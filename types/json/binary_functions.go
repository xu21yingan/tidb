// Copyright 2017 PingCAP, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package json

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"sort"
	"unicode/utf8"

	"github.com/pingcap/errors"
	"github.com/pingcap/tidb/util/hack"
	"github.com/pingcap/tidb/util/stringutil"
	"golang.org/x/exp/slices"
)

// Type returns type of BinaryJSON as string.
func (bj BinaryJSON) Type() string {
	switch bj.TypeCode {
	case TypeCodeObject:
		return "OBJECT"
	case TypeCodeArray:
		return "ARRAY"
	case TypeCodeLiteral:
		switch bj.Value[0] {
		case LiteralNil:
			return "NULL"
		default:
			return "BOOLEAN"
		}
	case TypeCodeInt64:
		return "INTEGER"
	case TypeCodeUint64:
		return "UNSIGNED INTEGER"
	case TypeCodeFloat64:
		return "DOUBLE"
	case TypeCodeString:
		return "STRING"
	default:
		msg := fmt.Sprintf(unknownTypeCodeErrorMsg, bj.TypeCode)
		panic(msg)
	}
}

// Unquote is for JSON_UNQUOTE.
func (bj BinaryJSON) Unquote() (string, error) {
	switch bj.TypeCode {
	case TypeCodeString:
		str := string(hack.String(bj.GetString()))
		return UnquoteString(str)
	default:
		return bj.String(), nil
	}
}

// UnquoteString remove quotes in a string,
// including the quotes at the head and tail of string.
func UnquoteString(str string) (string, error) {
	strLen := len(str)
	if strLen < 2 {
		return str, nil
	}
	head, tail := str[0], str[strLen-1]
	if head == '"' && tail == '"' {
		// Remove prefix and suffix '"' before unquoting
		return unquoteString(str[1 : strLen-1])
	}
	// if value is not double quoted, do nothing
	return str, nil
}

// unquoteString recognizes the escape sequences shown in:
// https://dev.mysql.com/doc/refman/5.7/en/json-modification-functions.html#json-unquote-character-escape-sequences
func unquoteString(s string) (string, error) {
	ret := new(bytes.Buffer)
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' {
			i++
			if i == len(s) {
				return "", errors.New("Missing a closing quotation mark in string")
			}
			switch s[i] {
			case '"':
				ret.WriteByte('"')
			case 'b':
				ret.WriteByte('\b')
			case 'f':
				ret.WriteByte('\f')
			case 'n':
				ret.WriteByte('\n')
			case 'r':
				ret.WriteByte('\r')
			case 't':
				ret.WriteByte('\t')
			case '\\':
				ret.WriteByte('\\')
			case 'u':
				if i+4 > len(s) {
					return "", errors.Errorf("Invalid unicode: %s", s[i+1:])
				}
				char, size, err := decodeEscapedUnicode(hack.Slice(s[i+1 : i+5]))
				if err != nil {
					return "", errors.Trace(err)
				}
				ret.Write(char[0:size])
				i += 4
			default:
				// For all other escape sequences, backslash is ignored.
				ret.WriteByte(s[i])
			}
		} else {
			ret.WriteByte(s[i])
		}
	}
	return ret.String(), nil
}

// decodeEscapedUnicode decodes unicode into utf8 bytes specified in RFC 3629.
// According RFC 3629, the max length of utf8 characters is 4 bytes.
// And MySQL use 4 bytes to represent the unicode which must be in [0, 65536).
func decodeEscapedUnicode(s []byte) (char [4]byte, size int, err error) {
	size, err = hex.Decode(char[0:2], s)
	if err != nil || size != 2 {
		// The unicode must can be represented in 2 bytes.
		return char, 0, errors.Trace(err)
	}

	unicode := binary.BigEndian.Uint16(char[0:2])
	size = utf8.RuneLen(rune(unicode))
	utf8.EncodeRune(char[0:size], rune(unicode))
	return
}

// quoteString escapes interior quote and other characters for JSON_QUOTE
// https://dev.mysql.com/doc/refman/5.7/en/json-creation-functions.html#function_json-quote
// TODO: add JSON_QUOTE builtin
func quoteString(s string) string {
	var escapeByteMap = map[byte]string{
		'\\': "\\\\",
		'"':  "\\\"",
		'\b': "\\b",
		'\f': "\\f",
		'\n': "\\n",
		'\r': "\\r",
		'\t': "\\t",
	}

	ret := new(bytes.Buffer)
	ret.WriteByte('"')

	start := 0
	hasEscaped := false

	for i := 0; i < len(s); {
		if b := s[i]; b < utf8.RuneSelf {
			escaped, ok := escapeByteMap[b]
			if ok {
				if start < i {
					ret.WriteString(s[start:i])
				}
				hasEscaped = true
				ret.WriteString(escaped)
				i++
				start = i
			} else {
				i++
			}
		} else {
			c, size := utf8.DecodeRune([]byte(s[i:]))
			if c == utf8.RuneError && size == 1 { // refer to codes of `binary.marshalStringTo`
				if start < i {
					ret.WriteString(s[start:i])
				}
				hasEscaped = true
				ret.WriteString(`\ufffd`)
				i += size
				start = i
				continue
			}
			i += size
		}
	}

	if start < len(s) {
		ret.WriteString(s[start:])
	}

	if hasEscaped {
		ret.WriteByte('"')
		return ret.String()
	}
	return ret.String()[1:]
}

// Extract receives several path expressions as arguments, matches them in bj, and returns:
//  ret: target JSON matched any path expressions. maybe autowrapped as an array.
//  found: true if any path expressions matched.
func (bj BinaryJSON) Extract(pathExprList []PathExpression) (ret BinaryJSON, found bool) {
	buf := make([]BinaryJSON, 0, 1)
	for _, pathExpr := range pathExprList {
		buf = bj.extractTo(buf, pathExpr)
	}
	if len(buf) == 0 {
		found = false
	} else if len(pathExprList) == 1 && len(buf) == 1 {
		// If pathExpr contains asterisks, len(elemList) won't be 1
		// even if len(pathExprList) equals to 1.
		found = true
		ret = buf[0]
		// Fix https://github.com/pingcap/tidb/issues/30352
		if pathExprList[0].ContainsAnyAsterisk() {
			ret = buildBinaryArray(buf)
		}
	} else {
		found = true
		ret = buildBinaryArray(buf)
	}
	return
}

func (bj BinaryJSON) extractTo(buf []BinaryJSON, pathExpr PathExpression) []BinaryJSON {
	if len(pathExpr.legs) == 0 {
		return append(buf, bj)
	}
	currentLeg, subPathExpr := pathExpr.popOneLeg()
	if currentLeg.typ == pathLegIndex {
		if bj.TypeCode != TypeCodeArray {
			if currentLeg.arrayIndex <= 0 && currentLeg.arrayIndex != arrayIndexAsterisk {
				buf = bj.extractTo(buf, subPathExpr)
			}
			return buf
		}
		elemCount := bj.GetElemCount()
		if currentLeg.arrayIndex == arrayIndexAsterisk {
			for i := 0; i < elemCount; i++ {
				buf = bj.arrayGetElem(i).extractTo(buf, subPathExpr)
			}
		} else if currentLeg.arrayIndex < elemCount {
			buf = bj.arrayGetElem(currentLeg.arrayIndex).extractTo(buf, subPathExpr)
		}
	} else if currentLeg.typ == pathLegKey && bj.TypeCode == TypeCodeObject {
		elemCount := bj.GetElemCount()
		if currentLeg.dotKey == "*" {
			for i := 0; i < elemCount; i++ {
				buf = bj.objectGetVal(i).extractTo(buf, subPathExpr)
			}
		} else {
			child, ok := bj.objectSearchKey(hack.Slice(currentLeg.dotKey))
			if ok {
				buf = child.extractTo(buf, subPathExpr)
			}
		}
	} else if currentLeg.typ == pathLegDoubleAsterisk {
		buf = bj.extractTo(buf, subPathExpr)
		if bj.TypeCode == TypeCodeArray {
			elemCount := bj.GetElemCount()
			for i := 0; i < elemCount; i++ {
				buf = bj.arrayGetElem(i).extractTo(buf, pathExpr)
			}
		} else if bj.TypeCode == TypeCodeObject {
			elemCount := bj.GetElemCount()
			for i := 0; i < elemCount; i++ {
				buf = bj.objectGetVal(i).extractTo(buf, pathExpr)
			}
		}
	}
	return buf
}

func (bj BinaryJSON) objectSearchKey(key []byte) (BinaryJSON, bool) {
	elemCount := bj.GetElemCount()
	idx := sort.Search(elemCount, func(i int) bool {
		return bytes.Compare(bj.objectGetKey(i), key) >= 0
	})
	if idx < elemCount && bytes.Equal(bj.objectGetKey(idx), key) {
		return bj.objectGetVal(idx), true
	}
	return BinaryJSON{}, false
}

func buildBinaryArray(elems []BinaryJSON) BinaryJSON {
	totalSize := headerSize + len(elems)*valEntrySize
	for _, elem := range elems {
		if elem.TypeCode != TypeCodeLiteral {
			totalSize += len(elem.Value)
		}
	}
	buf := make([]byte, headerSize+len(elems)*valEntrySize, totalSize)
	endian.PutUint32(buf, uint32(len(elems)))
	endian.PutUint32(buf[dataSizeOff:], uint32(totalSize))
	buf = buildBinaryElements(buf, headerSize, elems)
	return BinaryJSON{TypeCode: TypeCodeArray, Value: buf}
}

func buildBinaryElements(buf []byte, entryStart int, elems []BinaryJSON) []byte {
	for i, elem := range elems {
		buf[entryStart+i*valEntrySize] = elem.TypeCode
		if elem.TypeCode == TypeCodeLiteral {
			buf[entryStart+i*valEntrySize+valTypeSize] = elem.Value[0]
		} else {
			endian.PutUint32(buf[entryStart+i*valEntrySize+valTypeSize:], uint32(len(buf)))
			buf = append(buf, elem.Value...)
		}
	}
	return buf
}

func buildBinaryObject(keys [][]byte, elems []BinaryJSON) (BinaryJSON, error) {
	totalSize := headerSize + len(elems)*(keyEntrySize+valEntrySize)
	for i, elem := range elems {
		if elem.TypeCode != TypeCodeLiteral {
			totalSize += len(elem.Value)
		}
		totalSize += len(keys[i])
	}
	buf := make([]byte, headerSize+len(elems)*(keyEntrySize+valEntrySize), totalSize)
	endian.PutUint32(buf, uint32(len(elems)))
	endian.PutUint32(buf[dataSizeOff:], uint32(totalSize))
	for i, key := range keys {
		if len(key) > math.MaxUint16 {
			return BinaryJSON{}, ErrJSONObjectKeyTooLong
		}
		endian.PutUint32(buf[headerSize+i*keyEntrySize:], uint32(len(buf)))
		endian.PutUint16(buf[headerSize+i*keyEntrySize+keyLenOff:], uint16(len(key)))
		buf = append(buf, key...)
	}
	entryStart := headerSize + len(elems)*keyEntrySize
	buf = buildBinaryElements(buf, entryStart, elems)
	return BinaryJSON{TypeCode: TypeCodeObject, Value: buf}, nil
}

// Modify modifies a JSON object by insert, replace or set.
// All path expressions cannot contain * or ** wildcard.
// If any error occurs, the input won't be changed.
func (bj BinaryJSON) Modify(pathExprList []PathExpression, values []BinaryJSON, mt ModifyType) (retj BinaryJSON, err error) {
	if len(pathExprList) != len(values) {
		// TODO: should return 1582(42000)
		return retj, errors.New("Incorrect parameter count")
	}
	for _, pathExpr := range pathExprList {
		if pathExpr.flags.containsAnyAsterisk() {
			// TODO: should return 3149(42000)
			return retj, errors.New("Invalid path expression")
		}
	}
	for i := 0; i < len(pathExprList); i++ {
		pathExpr, value := pathExprList[i], values[i]
		modifier := &binaryModifier{bj: bj}
		switch mt {
		case ModifyInsert:
			bj = modifier.insert(pathExpr, value)
		case ModifyReplace:
			bj = modifier.replace(pathExpr, value)
		case ModifySet:
			bj = modifier.set(pathExpr, value)
		}
		if modifier.err != nil {
			return BinaryJSON{}, modifier.err
		}
	}
	return bj, nil
}

// ArrayInsert insert a BinaryJSON into the given array cell.
// All path expressions cannot contain * or ** wildcard.
// If any error occurs, the input won't be changed.
func (bj BinaryJSON) ArrayInsert(pathExpr PathExpression, value BinaryJSON) (res BinaryJSON, err error) {
	// Check the path is a index
	if len(pathExpr.legs) < 1 {
		return bj, ErrInvalidJSONPathArrayCell
	}
	parentPath, lastLeg := pathExpr.popOneLastLeg()
	if lastLeg.typ != pathLegIndex {
		return bj, ErrInvalidJSONPathArrayCell
	}
	// Find the target array
	obj, exists := bj.Extract([]PathExpression{parentPath})
	if !exists || obj.TypeCode != TypeCodeArray {
		return bj, nil
	}

	idx := lastLeg.arrayIndex
	count := obj.GetElemCount()
	if idx >= count {
		idx = count
	}
	// Insert into the array
	newArray := make([]BinaryJSON, 0, count+1)
	for i := 0; i < idx; i++ {
		elem := obj.arrayGetElem(i)
		newArray = append(newArray, elem)
	}
	newArray = append(newArray, value)
	for i := idx; i < count; i++ {
		elem := obj.arrayGetElem(i)
		newArray = append(newArray, elem)
	}
	obj = buildBinaryArray(newArray)

	bj, err = bj.Modify([]PathExpression{parentPath}, []BinaryJSON{obj}, ModifySet)
	if err != nil {
		return bj, err
	}
	return bj, nil
}

// Remove removes the elements indicated by pathExprList from JSON.
func (bj BinaryJSON) Remove(pathExprList []PathExpression) (BinaryJSON, error) {
	for _, pathExpr := range pathExprList {
		if len(pathExpr.legs) == 0 {
			// TODO: should return 3153(42000)
			return bj, errors.New("Invalid path expression")
		}
		if pathExpr.flags.containsAnyAsterisk() {
			// TODO: should return 3149(42000)
			return bj, errors.New("Invalid path expression")
		}
		modifer := &binaryModifier{bj: bj}
		bj = modifer.remove(pathExpr)
		if modifer.err != nil {
			return BinaryJSON{}, modifer.err
		}
	}
	return bj, nil
}

type binaryModifier struct {
	bj          BinaryJSON
	modifyPtr   *byte
	modifyValue BinaryJSON
	err         error
}

func (bm *binaryModifier) set(path PathExpression, newBj BinaryJSON) BinaryJSON {
	result := make([]BinaryJSON, 0, 1)
	result = bm.bj.extractTo(result, path)
	if len(result) > 0 {
		bm.modifyPtr = &result[0].Value[0]
		bm.modifyValue = newBj
		return bm.rebuild()
	}
	bm.doInsert(path, newBj)
	if bm.err != nil {
		return BinaryJSON{}
	}
	return bm.rebuild()
}

func (bm *binaryModifier) replace(path PathExpression, newBj BinaryJSON) BinaryJSON {
	result := make([]BinaryJSON, 0, 1)
	result = bm.bj.extractTo(result, path)
	if len(result) == 0 {
		return bm.bj
	}
	bm.modifyPtr = &result[0].Value[0]
	bm.modifyValue = newBj
	return bm.rebuild()
}

func (bm *binaryModifier) insert(path PathExpression, newBj BinaryJSON) BinaryJSON {
	result := make([]BinaryJSON, 0, 1)
	result = bm.bj.extractTo(result, path)
	if len(result) > 0 {
		return bm.bj
	}
	bm.doInsert(path, newBj)
	if bm.err != nil {
		return BinaryJSON{}
	}
	return bm.rebuild()
}

// doInsert inserts the newBj to its parent, and builds the new parent.
func (bm *binaryModifier) doInsert(path PathExpression, newBj BinaryJSON) {
	parentPath, lastLeg := path.popOneLastLeg()
	result := make([]BinaryJSON, 0, 1)
	result = bm.bj.extractTo(result, parentPath)
	if len(result) == 0 {
		return
	}
	parentBj := result[0]
	if lastLeg.typ == pathLegIndex {
		bm.modifyPtr = &parentBj.Value[0]
		if parentBj.TypeCode != TypeCodeArray {
			bm.modifyValue = buildBinaryArray([]BinaryJSON{parentBj, newBj})
			return
		}
		elemCount := parentBj.GetElemCount()
		elems := make([]BinaryJSON, 0, elemCount+1)
		for i := 0; i < elemCount; i++ {
			elems = append(elems, parentBj.arrayGetElem(i))
		}
		elems = append(elems, newBj)
		bm.modifyValue = buildBinaryArray(elems)
		return
	}
	if parentBj.TypeCode != TypeCodeObject {
		return
	}
	bm.modifyPtr = &parentBj.Value[0]
	elemCount := parentBj.GetElemCount()
	insertKey := hack.Slice(lastLeg.dotKey)
	insertIdx := sort.Search(elemCount, func(i int) bool {
		return bytes.Compare(parentBj.objectGetKey(i), insertKey) >= 0
	})
	keys := make([][]byte, 0, elemCount+1)
	elems := make([]BinaryJSON, 0, elemCount+1)
	for i := 0; i < elemCount; i++ {
		if i == insertIdx {
			keys = append(keys, insertKey)
			elems = append(elems, newBj)
		}
		keys = append(keys, parentBj.objectGetKey(i))
		elems = append(elems, parentBj.objectGetVal(i))
	}
	if insertIdx == elemCount {
		keys = append(keys, insertKey)
		elems = append(elems, newBj)
	}
	bm.modifyValue, bm.err = buildBinaryObject(keys, elems)
}

func (bm *binaryModifier) remove(path PathExpression) BinaryJSON {
	result := make([]BinaryJSON, 0, 1)
	result = bm.bj.extractTo(result, path)
	if len(result) == 0 {
		return bm.bj
	}
	bm.doRemove(path)
	if bm.err != nil {
		return BinaryJSON{}
	}
	return bm.rebuild()
}

func (bm *binaryModifier) doRemove(path PathExpression) {
	parentPath, lastLeg := path.popOneLastLeg()
	result := make([]BinaryJSON, 0, 1)
	result = bm.bj.extractTo(result, parentPath)
	if len(result) == 0 {
		return
	}
	parentBj := result[0]
	if lastLeg.typ == pathLegIndex {
		if parentBj.TypeCode != TypeCodeArray {
			return
		}
		bm.modifyPtr = &parentBj.Value[0]
		elemCount := parentBj.GetElemCount()
		elems := make([]BinaryJSON, 0, elemCount-1)
		for i := 0; i < elemCount; i++ {
			if i != lastLeg.arrayIndex {
				elems = append(elems, parentBj.arrayGetElem(i))
			}
		}
		bm.modifyValue = buildBinaryArray(elems)
		return
	}
	if parentBj.TypeCode != TypeCodeObject {
		return
	}
	bm.modifyPtr = &parentBj.Value[0]
	elemCount := parentBj.GetElemCount()
	removeKey := hack.Slice(lastLeg.dotKey)
	keys := make([][]byte, 0, elemCount+1)
	elems := make([]BinaryJSON, 0, elemCount+1)
	for i := 0; i < elemCount; i++ {
		key := parentBj.objectGetKey(i)
		if !bytes.Equal(key, removeKey) {
			keys = append(keys, parentBj.objectGetKey(i))
			elems = append(elems, parentBj.objectGetVal(i))
		}
	}
	bm.modifyValue, bm.err = buildBinaryObject(keys, elems)
}

// rebuild merges the old and the modified JSON into a new BinaryJSON
func (bm *binaryModifier) rebuild() BinaryJSON {
	buf := make([]byte, 0, len(bm.bj.Value)+len(bm.modifyValue.Value))
	value, tpCode := bm.rebuildTo(buf)
	return BinaryJSON{TypeCode: tpCode, Value: value}
}

func (bm *binaryModifier) rebuildTo(buf []byte) ([]byte, TypeCode) {
	if bm.modifyPtr == &bm.bj.Value[0] {
		bm.modifyPtr = nil
		return append(buf, bm.modifyValue.Value...), bm.modifyValue.TypeCode
	} else if bm.modifyPtr == nil {
		return append(buf, bm.bj.Value...), bm.bj.TypeCode
	}
	bj := bm.bj
	switch bj.TypeCode {
	case TypeCodeLiteral, TypeCodeInt64, TypeCodeUint64, TypeCodeFloat64, TypeCodeString:
		return append(buf, bj.Value...), bj.TypeCode
	}
	docOff := len(buf)
	elemCount := bj.GetElemCount()
	var valEntryStart int
	if bj.TypeCode == TypeCodeArray {
		copySize := headerSize + elemCount*valEntrySize
		valEntryStart = headerSize
		buf = append(buf, bj.Value[:copySize]...)
	} else {
		copySize := headerSize + elemCount*(keyEntrySize+valEntrySize)
		valEntryStart = headerSize + elemCount*keyEntrySize
		buf = append(buf, bj.Value[:copySize]...)
		if elemCount > 0 {
			firstKeyOff := int(endian.Uint32(bj.Value[headerSize:]))
			lastKeyOff := int(endian.Uint32(bj.Value[headerSize+(elemCount-1)*keyEntrySize:]))
			lastKeyLen := int(endian.Uint16(bj.Value[headerSize+(elemCount-1)*keyEntrySize+keyLenOff:]))
			buf = append(buf, bj.Value[firstKeyOff:lastKeyOff+lastKeyLen]...)
		}
	}
	for i := 0; i < elemCount; i++ {
		valEntryOff := valEntryStart + i*valEntrySize
		elem := bj.valEntryGet(valEntryOff)
		bm.bj = elem
		var tpCode TypeCode
		valOff := len(buf) - docOff
		buf, tpCode = bm.rebuildTo(buf)
		buf[docOff+valEntryOff] = tpCode
		if tpCode == TypeCodeLiteral {
			lastIdx := len(buf) - 1
			endian.PutUint32(buf[docOff+valEntryOff+valTypeSize:], uint32(buf[lastIdx]))
			buf = buf[:lastIdx]
		} else {
			endian.PutUint32(buf[docOff+valEntryOff+valTypeSize:], uint32(valOff))
		}
	}
	endian.PutUint32(buf[docOff+dataSizeOff:], uint32(len(buf)-docOff))
	return buf, bj.TypeCode
}

// floatEpsilon is the acceptable error quantity when comparing two float numbers.
const floatEpsilon = 1.e-8

// compareFloat64PrecisionLoss returns an integer comparing the float64 x to y,
// allowing precision loss.
func compareFloat64PrecisionLoss(x, y float64) int {
	if x-y < floatEpsilon && y-x < floatEpsilon {
		return 0
	} else if x-y < 0 {
		return -1
	}
	return 1
}

func compareInt64(x int64, y int64) int {
	if x < y {
		return -1
	} else if x == y {
		return 0
	}

	return 1
}

func compareFloat64(x float64, y float64) int {
	if x < y {
		return -1
	} else if x == y {
		return 0
	}

	return 1
}

func compareUint64(x uint64, y uint64) int {
	if x < y {
		return -1
	} else if x == y {
		return 0
	}

	return 1
}

func compareInt64Uint64(x int64, y uint64) int {
	if x < 0 {
		return -1
	}
	return compareUint64(uint64(x), y)
}

func compareFloat64Int64(x float64, y int64) int {
	return compareFloat64PrecisionLoss(x, float64(y))
}

func compareFloat64Uint64(x float64, y uint64) int {
	return compareFloat64PrecisionLoss(x, float64(y))
}

// CompareBinary compares two binary json objects. Returns -1 if left < right,
// 0 if left == right, else returns 1.
func CompareBinary(left, right BinaryJSON) int {
	precedence1 := jsonTypePrecedences[left.Type()]
	precedence2 := jsonTypePrecedences[right.Type()]
	var cmp int
	if precedence1 == precedence2 {
		if precedence1 == jsonTypePrecedences["NULL"] {
			// for JSON null.
			cmp = 0
		}
		switch left.TypeCode {
		case TypeCodeLiteral:
			// false is less than true.
			cmp = int(right.Value[0]) - int(left.Value[0])
		case TypeCodeInt64:
			switch right.TypeCode {
			case TypeCodeInt64:
				cmp = compareInt64(left.GetInt64(), right.GetInt64())
			case TypeCodeUint64:
				cmp = compareInt64Uint64(left.GetInt64(), right.GetUint64())
			case TypeCodeFloat64:
				cmp = -compareFloat64Int64(right.GetFloat64(), left.GetInt64())
			}
		case TypeCodeUint64:
			switch right.TypeCode {
			case TypeCodeInt64:
				cmp = -compareInt64Uint64(right.GetInt64(), left.GetUint64())
			case TypeCodeUint64:
				cmp = compareUint64(left.GetUint64(), right.GetUint64())
			case TypeCodeFloat64:
				cmp = -compareFloat64Uint64(right.GetFloat64(), left.GetUint64())
			}
		case TypeCodeFloat64:
			switch right.TypeCode {
			case TypeCodeInt64:
				cmp = compareFloat64Int64(left.GetFloat64(), right.GetInt64())
			case TypeCodeUint64:
				cmp = compareFloat64Uint64(left.GetFloat64(), right.GetUint64())
			case TypeCodeFloat64:
				cmp = compareFloat64(left.GetFloat64(), right.GetFloat64())
			}
		case TypeCodeString:
			cmp = bytes.Compare(left.GetString(), right.GetString())
		case TypeCodeArray:
			leftCount := left.GetElemCount()
			rightCount := right.GetElemCount()
			for i := 0; i < leftCount && i < rightCount; i++ {
				elem1 := left.arrayGetElem(i)
				elem2 := right.arrayGetElem(i)
				cmp = CompareBinary(elem1, elem2)
				if cmp != 0 {
					return cmp
				}
			}
			cmp = leftCount - rightCount
		case TypeCodeObject:
			// reference:
			// https://github.com/mysql/mysql-server/blob/ee4455a33b10f1b1886044322e4893f587b319ed/sql/json_dom.cc#L2561
			leftCount, rightCount := left.GetElemCount(), right.GetElemCount()
			cmp := compareInt64(int64(leftCount), int64(rightCount))
			if cmp != 0 {
				return cmp
			}
			for i := 0; i < leftCount; i++ {
				leftKey, rightKey := left.objectGetKey(i), right.objectGetKey(i)
				cmp = bytes.Compare(leftKey, rightKey)
				if cmp != 0 {
					return cmp
				}
				cmp = CompareBinary(left.objectGetVal(i), right.objectGetVal(i))
				if cmp != 0 {
					return cmp
				}
			}
		}
	} else {
		cmp = precedence1 - precedence2
	}
	return cmp
}

// MergePatchBinary implements RFC7396
// https://datatracker.ietf.org/doc/html/rfc7396
func MergePatchBinary(bjs []*BinaryJSON) (*BinaryJSON, error) {
	var err error
	length := len(bjs)

	// according to the implements of RFC7396
	// when the last item is not object
	// we can return the last item directly
	for i := length - 1; i >= 0; i-- {
		if bjs[i] == nil || bjs[i].TypeCode != TypeCodeObject {
			bjs = bjs[i:]
			break
		}
	}

	target := bjs[0]
	for _, patch := range bjs[1:] {
		target, err = mergePatchBinary(target, patch)
		if err != nil {
			return nil, err
		}
	}
	return target, nil
}

func mergePatchBinary(target, patch *BinaryJSON) (result *BinaryJSON, err error) {
	if patch == nil {
		return nil, nil
	}

	if patch.TypeCode == TypeCodeObject {
		if target == nil {
			return nil, nil
		}

		keyValMap := make(map[string]BinaryJSON)
		if target.TypeCode == TypeCodeObject {
			elemCount := target.GetElemCount()
			for i := 0; i < elemCount; i++ {
				key := target.objectGetKey(i)
				val := target.objectGetVal(i)
				keyValMap[string(key)] = val
			}
		}
		var tmp *BinaryJSON
		elemCount := patch.GetElemCount()
		for i := 0; i < elemCount; i++ {
			key := patch.objectGetKey(i)
			val := patch.objectGetVal(i)
			k := string(key)

			targetKV, exists := keyValMap[k]
			if val.TypeCode == TypeCodeLiteral && val.Value[0] == LiteralNil {
				if exists {
					delete(keyValMap, k)
				}
			} else {
				tmp, err = mergePatchBinary(&targetKV, &val)
				if err != nil {
					return result, err
				}

				keyValMap[k] = *tmp
			}
		}

		length := len(keyValMap)
		keys := make([][]byte, 0, length)
		for key := range keyValMap {
			keys = append(keys, []byte(key))
		}
		slices.SortFunc(keys, func(i, j []byte) bool {
			return bytes.Compare(i, j) < 0
		})
		length = len(keys)
		values := make([]BinaryJSON, 0, len(keys))
		for i := 0; i < length; i++ {
			values = append(values, keyValMap[string(keys[i])])
		}

		binaryObject, e := buildBinaryObject(keys, values)
		if e != nil {
			return nil, e
		}
		return &binaryObject, nil
	}
	return patch, nil
}

// MergeBinary merges multiple BinaryJSON into one according the following rules:
// 1) adjacent arrays are merged to a single array;
// 2) adjacent object are merged to a single object;
// 3) a scalar value is autowrapped as an array before merge;
// 4) an adjacent array and object are merged by autowrapping the object as an array.
func MergeBinary(bjs []BinaryJSON) BinaryJSON {
	var remain = bjs
	var objects []BinaryJSON
	var results []BinaryJSON
	for len(remain) > 0 {
		if remain[0].TypeCode != TypeCodeObject {
			results = append(results, remain[0])
			remain = remain[1:]
		} else {
			objects, remain = getAdjacentObjects(remain)
			results = append(results, mergeBinaryObject(objects))
		}
	}
	if len(results) == 1 {
		return results[0]
	}
	return mergeBinaryArray(results)
}

func getAdjacentObjects(bjs []BinaryJSON) (objects, remain []BinaryJSON) {
	for i := 0; i < len(bjs); i++ {
		if bjs[i].TypeCode != TypeCodeObject {
			return bjs[:i], bjs[i:]
		}
	}
	return bjs, nil
}

func mergeBinaryArray(elems []BinaryJSON) BinaryJSON {
	buf := make([]BinaryJSON, 0, len(elems))
	for i := 0; i < len(elems); i++ {
		elem := elems[i]
		if elem.TypeCode != TypeCodeArray {
			buf = append(buf, elem)
		} else {
			childCount := elem.GetElemCount()
			for j := 0; j < childCount; j++ {
				buf = append(buf, elem.arrayGetElem(j))
			}
		}
	}
	return buildBinaryArray(buf)
}

func mergeBinaryObject(objects []BinaryJSON) BinaryJSON {
	keyValMap := make(map[string]BinaryJSON)
	keys := make([][]byte, 0, len(keyValMap))
	for _, obj := range objects {
		elemCount := obj.GetElemCount()
		for i := 0; i < elemCount; i++ {
			key := obj.objectGetKey(i)
			val := obj.objectGetVal(i)
			if old, ok := keyValMap[string(key)]; ok {
				keyValMap[string(key)] = MergeBinary([]BinaryJSON{old, val})
			} else {
				keyValMap[string(key)] = val
				keys = append(keys, key)
			}
		}
	}
	slices.SortFunc(keys, func(i, j []byte) bool {
		return bytes.Compare(i, j) < 0
	})
	values := make([]BinaryJSON, len(keys))
	for i, key := range keys {
		values[i] = keyValMap[string(key)]
	}
	binaryObject, err := buildBinaryObject(keys, values)
	if err != nil {
		panic("mergeBinaryObject should never panic, please contact the TiDB team for help")
	}
	return binaryObject
}

// PeekBytesAsJSON trys to peek some bytes from b, until
// we can deserialize a JSON from those bytes.
func PeekBytesAsJSON(b []byte) (n int, err error) {
	if len(b) <= 0 {
		err = errors.New("Cant peek from empty bytes")
		return
	}
	switch c := b[0]; c {
	case TypeCodeObject, TypeCodeArray:
		if len(b) >= valTypeSize+headerSize {
			size := endian.Uint32(b[valTypeSize+dataSizeOff:])
			n = valTypeSize + int(size)
			return
		}
	case TypeCodeString:
		strLen, lenLen := binary.Uvarint(b[valTypeSize:])
		return valTypeSize + int(strLen) + lenLen, nil
	case TypeCodeInt64, TypeCodeUint64, TypeCodeFloat64:
		n = valTypeSize + 8
		return
	case TypeCodeLiteral:
		n = valTypeSize + 1
		return
	}
	err = errors.New("Invalid JSON bytes")
	return
}

// ContainsBinary check whether JSON document contains specific target according the following rules:
// 1) object contains a target object if and only if every key is contained in source object and the value associated with the target key is contained in the value associated with the source key;
// 2) array contains a target nonarray if and only if the target is contained in some element of the array;
// 3) array contains a target array if and only if every element is contained in some element of the array;
// 4) scalar contains a target scalar if and only if they are comparable and are equal;
func ContainsBinary(obj, target BinaryJSON) bool {
	switch obj.TypeCode {
	case TypeCodeObject:
		if target.TypeCode == TypeCodeObject {
			elemCount := target.GetElemCount()
			for i := 0; i < elemCount; i++ {
				key := target.objectGetKey(i)
				val := target.objectGetVal(i)
				if exp, exists := obj.objectSearchKey(key); !exists || !ContainsBinary(exp, val) {
					return false
				}
			}
			return true
		}
		return false
	case TypeCodeArray:
		if target.TypeCode == TypeCodeArray {
			elemCount := target.GetElemCount()
			for i := 0; i < elemCount; i++ {
				if !ContainsBinary(obj, target.arrayGetElem(i)) {
					return false
				}
			}
			return true
		}
		elemCount := obj.GetElemCount()
		for i := 0; i < elemCount; i++ {
			if ContainsBinary(obj.arrayGetElem(i), target) {
				return true
			}
		}
		return false
	default:
		return CompareBinary(obj, target) == 0
	}
}

// GetElemDepth for JSON_DEPTH
// Returns the maximum depth of a JSON document
// rules referenced by MySQL JSON_DEPTH function
// [https://dev.mysql.com/doc/refman/5.7/en/json-attribute-functions.html#function_json-depth]
// 1) An empty array, empty object, or scalar value has depth 1.
// 2) A nonempty array containing only elements of depth 1 or nonempty object containing only member values of depth 1 has depth 2.
// 3) Otherwise, a JSON document has depth greater than 2.
// e.g. depth of '{}', '[]', 'true': 1
// e.g. depth of '[10, 20]', '[[], {}]': 2
// e.g. depth of '[10, {"a": 20}]': 3
func (bj BinaryJSON) GetElemDepth() int {
	switch bj.TypeCode {
	case TypeCodeObject:
		elemCount := bj.GetElemCount()
		maxDepth := 0
		for i := 0; i < elemCount; i++ {
			obj := bj.objectGetVal(i)
			depth := obj.GetElemDepth()
			if depth > maxDepth {
				maxDepth = depth
			}
		}
		return maxDepth + 1
	case TypeCodeArray:
		elemCount := bj.GetElemCount()
		maxDepth := 0
		for i := 0; i < elemCount; i++ {
			obj := bj.arrayGetElem(i)
			depth := obj.GetElemDepth()
			if depth > maxDepth {
				maxDepth = depth
			}
		}
		return maxDepth + 1
	default:
		return 1
	}
}

// Search for JSON_Search
// rules referenced by MySQL JSON_SEARCH function
// [https://dev.mysql.com/doc/refman/5.7/en/json-search-functions.html#function_json-search]
func (bj BinaryJSON) Search(containType string, search string, escape byte, pathExpres []PathExpression) (res BinaryJSON, isNull bool, err error) {
	if containType != ContainsPathOne && containType != ContainsPathAll {
		return res, true, ErrInvalidJSONPath
	}
	patChars, patTypes := stringutil.CompilePattern(search, escape)

	result := make([]interface{}, 0)
	walkFn := func(fullpath PathExpression, bj BinaryJSON) (stop bool, err error) {
		if bj.TypeCode == TypeCodeString && stringutil.DoMatch(string(bj.GetString()), patChars, patTypes) {
			result = append(result, fullpath.String())
			if containType == ContainsPathOne {
				return true, nil
			}
		}
		return false, nil
	}
	if len(pathExpres) != 0 {
		err := bj.Walk(walkFn, pathExpres...)
		if err != nil {
			return res, true, err
		}
	} else {
		err := bj.Walk(walkFn)
		if err != nil {
			return res, true, err
		}
	}
	switch len(result) {
	case 0:
		return res, true, nil
	case 1:
		return CreateBinary(result[0]), false, nil
	default:
		return CreateBinary(result), false, nil
	}

}

// extractCallbackFn the type of CALLBACK function for extractToCallback
type extractCallbackFn func(fullpath PathExpression, bj BinaryJSON) (stop bool, err error)

// extractToCallback callback alternative of extractTo
//     would be more effective when walk through the whole JSON is unnecessary
// NOTICE: path [0] & [*] for JSON object other than array is INVALID, which is different from extractTo.
func (bj BinaryJSON) extractToCallback(pathExpr PathExpression, callbackFn extractCallbackFn, fullpath PathExpression) (stop bool, err error) {
	if len(pathExpr.legs) == 0 {
		return callbackFn(fullpath, bj)
	}

	currentLeg, subPathExpr := pathExpr.popOneLeg()
	if currentLeg.typ == pathLegIndex && bj.TypeCode == TypeCodeArray {
		elemCount := bj.GetElemCount()
		if currentLeg.arrayIndex == arrayIndexAsterisk {
			for i := 0; i < elemCount; i++ {
				// buf = bj.arrayGetElem(i).extractTo(buf, subPathExpr)
				path := fullpath.pushBackOneIndexLeg(i)
				stop, err = bj.arrayGetElem(i).extractToCallback(subPathExpr, callbackFn, path)
				if stop || err != nil {
					return
				}
			}
		} else if currentLeg.arrayIndex < elemCount {
			// buf = bj.arrayGetElem(currentLeg.arrayIndex).extractTo(buf, subPathExpr)
			path := fullpath.pushBackOneIndexLeg(currentLeg.arrayIndex)
			stop, err = bj.arrayGetElem(currentLeg.arrayIndex).extractToCallback(subPathExpr, callbackFn, path)
			if stop || err != nil {
				return
			}
		}
	} else if currentLeg.typ == pathLegKey && bj.TypeCode == TypeCodeObject {
		elemCount := bj.GetElemCount()
		if currentLeg.dotKey == "*" {
			for i := 0; i < elemCount; i++ {
				// buf = bj.objectGetVal(i).extractTo(buf, subPathExpr)
				path := fullpath.pushBackOneKeyLeg(string(bj.objectGetKey(i)))
				stop, err = bj.objectGetVal(i).extractToCallback(subPathExpr, callbackFn, path)
				if stop || err != nil {
					return
				}
			}
		} else {
			child, ok := bj.objectSearchKey(hack.Slice(currentLeg.dotKey))
			if ok {
				// buf = child.extractTo(buf, subPathExpr)
				path := fullpath.pushBackOneKeyLeg(currentLeg.dotKey)
				stop, err = child.extractToCallback(subPathExpr, callbackFn, path)
				if stop || err != nil {
					return
				}
			}
		}
	} else if currentLeg.typ == pathLegDoubleAsterisk {
		// buf = bj.extractTo(buf, subPathExpr)
		stop, err = bj.extractToCallback(subPathExpr, callbackFn, fullpath)
		if stop || err != nil {
			return
		}

		if bj.TypeCode == TypeCodeArray {
			elemCount := bj.GetElemCount()
			for i := 0; i < elemCount; i++ {
				// buf = bj.arrayGetElem(i).extractTo(buf, pathExpr)
				path := fullpath.pushBackOneIndexLeg(i)
				stop, err = bj.arrayGetElem(i).extractToCallback(pathExpr, callbackFn, path)
				if stop || err != nil {
					return
				}
			}
		} else if bj.TypeCode == TypeCodeObject {
			elemCount := bj.GetElemCount()
			for i := 0; i < elemCount; i++ {
				// buf = bj.objectGetVal(i).extractTo(buf, pathExpr)
				path := fullpath.pushBackOneKeyLeg(string(bj.objectGetKey(i)))
				stop, err = bj.objectGetVal(i).extractToCallback(pathExpr, callbackFn, path)
				if stop || err != nil {
					return
				}
			}
		}
	}
	return false, nil
}

// BinaryJSONWalkFunc is used as callback function for BinaryJSON.Walk
type BinaryJSONWalkFunc func(fullpath PathExpression, bj BinaryJSON) (stop bool, err error)

// Walk traverse BinaryJSON objects
func (bj BinaryJSON) Walk(walkFn BinaryJSONWalkFunc, pathExprList ...PathExpression) (err error) {
	pathSet := make(map[string]bool)

	var doWalk extractCallbackFn
	doWalk = func(fullpath PathExpression, bj BinaryJSON) (stop bool, err error) {
		pathStr := fullpath.String()
		if _, ok := pathSet[pathStr]; ok {
			return false, nil
		}

		stop, err = walkFn(fullpath, bj)
		pathSet[pathStr] = true
		if stop || err != nil {
			return
		}

		if bj.TypeCode == TypeCodeArray {
			elemCount := bj.GetElemCount()
			for i := 0; i < elemCount; i++ {
				path := fullpath.pushBackOneIndexLeg(i)
				stop, err = doWalk(path, bj.arrayGetElem(i))
				if stop || err != nil {
					return
				}
			}
		} else if bj.TypeCode == TypeCodeObject {
			elemCount := bj.GetElemCount()
			for i := 0; i < elemCount; i++ {
				path := fullpath.pushBackOneKeyLeg(string(bj.objectGetKey(i)))
				stop, err = doWalk(path, bj.objectGetVal(i))
				if stop || err != nil {
					return
				}
			}
		}
		return false, nil
	}

	fullpath := PathExpression{legs: make([]pathLeg, 0, 32), flags: pathExpressionFlag(0)}
	if len(pathExprList) > 0 {
		for _, pathExpr := range pathExprList {
			var stop bool
			stop, err = bj.extractToCallback(pathExpr, doWalk, fullpath)
			if stop || err != nil {
				return err
			}
		}
	} else {
		_, err = doWalk(fullpath, bj)
		if err != nil {
			return
		}
	}
	return nil
}
