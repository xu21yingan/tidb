// Copyright 2018 PingCAP, Inc.
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

package execdetails

import (
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/pingcap/tipb/go-tipb"
	"github.com/stretchr/testify/require"
	"github.com/tikv/client-go/v2/util"
)

func TestString(t *testing.T) {
	detail := &ExecDetails{
		CopTime:      time.Second + 3*time.Millisecond,
		BackoffTime:  time.Second,
		RequestCount: 1,
		CommitDetail: &util.CommitDetails{
			GetCommitTsTime: time.Second,
			PrewriteTime:    time.Second,
			CommitTime:      time.Second,
			LocalLatchTime:  time.Second,

			Mu: struct {
				sync.Mutex
				CommitBackoffTime int64
				BackoffTypes      []string
			}{
				CommitBackoffTime: int64(time.Second),
				BackoffTypes: []string{
					"backoff1",
					"backoff2",
				},
			},
			WriteKeys:         1,
			WriteSize:         1,
			PrewriteRegionNum: 1,
			TxnRetry:          1,
			ResolveLock: util.ResolveLockDetail{
				ResolveLockTime: 1000000000, // 10^9 ns = 1s
			},
		},
		ScanDetail: &util.ScanDetail{
			ProcessedKeys:             10,
			TotalKeys:                 100,
			RocksdbDeleteSkippedCount: 1,
			RocksdbKeySkippedCount:    1,
			RocksdbBlockCacheHitCount: 1,
			RocksdbBlockReadCount:     1,
			RocksdbBlockReadByte:      100,
		},
		TimeDetail: util.TimeDetail{
			ProcessTime: 2*time.Second + 5*time.Millisecond,
			WaitTime:    time.Second,
		},
	}
	expected := "Cop_time: 1.003 Process_time: 2.005 Wait_time: 1 Backoff_time: 1 Request_count: 1 Prewrite_time: 1 Commit_time: 1 " +
		"Get_commit_ts_time: 1 Commit_backoff_time: 1 Backoff_types: [backoff1 backoff2] Resolve_lock_time: 1 Local_latch_wait_time: 1 Write_keys: 1 Write_size: 1 Prewrite_region: 1 Txn_retry: 1 " +
		"Process_keys: 10 Total_keys: 100 Rocksdb_delete_skipped_count: 1 Rocksdb_key_skipped_count: 1 Rocksdb_block_cache_hit_count: 1 Rocksdb_block_read_count: 1 Rocksdb_block_read_byte: 100"
	require.Equal(t, expected, detail.String())
	detail = &ExecDetails{}
	require.Equal(t, "", detail.String())
}

func mockExecutorExecutionSummary(TimeProcessedNs, NumProducedRows, NumIterations uint64) *tipb.ExecutorExecutionSummary {
	return &tipb.ExecutorExecutionSummary{TimeProcessedNs: &TimeProcessedNs, NumProducedRows: &NumProducedRows,
		NumIterations: &NumIterations, XXX_unrecognized: nil}
}

func mockExecutorExecutionSummaryForTiFlash(TimeProcessedNs, NumProducedRows, NumIterations, Concurrency uint64, ExecutorID string) *tipb.ExecutorExecutionSummary {
	return &tipb.ExecutorExecutionSummary{TimeProcessedNs: &TimeProcessedNs, NumProducedRows: &NumProducedRows,
		NumIterations: &NumIterations, Concurrency: &Concurrency, ExecutorId: &ExecutorID, XXX_unrecognized: nil}
}

func TestCopRuntimeStats(t *testing.T) {
	stats := NewRuntimeStatsColl(nil)
	tableScanID := 1
	aggID := 2
	tableReaderID := 3
	stats.RecordOneCopTask(tableScanID, "tikv", "8.8.8.8", mockExecutorExecutionSummary(1, 1, 1))
	stats.RecordOneCopTask(tableScanID, "tikv", "8.8.8.9", mockExecutorExecutionSummary(2, 2, 2))
	stats.RecordOneCopTask(aggID, "tikv", "8.8.8.8", mockExecutorExecutionSummary(3, 3, 3))
	stats.RecordOneCopTask(aggID, "tikv", "8.8.8.9", mockExecutorExecutionSummary(4, 4, 4))
	scanDetail := &util.ScanDetail{
		TotalKeys:                 15,
		ProcessedKeys:             10,
		ProcessedKeysSize:         10,
		RocksdbDeleteSkippedCount: 5,
		RocksdbKeySkippedCount:    1,
		RocksdbBlockCacheHitCount: 10,
		RocksdbBlockReadCount:     20,
		RocksdbBlockReadByte:      100,
	}
	stats.RecordScanDetail(tableScanID, "tikv", scanDetail)
	require.True(t, stats.ExistsCopStats(tableScanID))

	cop := stats.GetOrCreateCopStats(tableScanID, "tikv")
	expected := "tikv_task:{proc max:2ns, min:1ns, avg: 1ns, p80:2ns, p95:2ns, iters:3, tasks:2}, " +
		"scan_detail: {total_process_keys: 10, total_process_keys_size: 10, total_keys: 15, rocksdb: {delete_skipped_count: 5, key_skipped_count: 1, block: {cache_hit_count: 10, read_count: 20, read_byte: 100 Bytes}}}"
	require.Equal(t, expected, cop.String())

	copStats := cop.stats["8.8.8.8"]
	require.NotNil(t, copStats)

	copStats[0].SetRowNum(10)
	copStats[0].Record(time.Second, 10)
	require.Equal(t, "time:1s, loops:2", copStats[0].String())
	require.Equal(t, "tikv_task:{proc max:4ns, min:3ns, avg: 3ns, p80:4ns, p95:4ns, iters:7, tasks:2}", stats.GetOrCreateCopStats(aggID, "tikv").String())

	rootStats := stats.GetRootStats(tableReaderID)
	require.NotNil(t, rootStats)
	require.True(t, stats.ExistsRootStats(tableReaderID))

	cop.scanDetail.ProcessedKeys = 0
	cop.scanDetail.ProcessedKeysSize = 0
	cop.scanDetail.RocksdbKeySkippedCount = 0
	cop.scanDetail.RocksdbBlockReadCount = 0
	// Print all fields even though the value of some fields is 0.
	str := "tikv_task:{proc max:1s, min:2ns, avg: 500ms, p80:1s, p95:1s, iters:4, tasks:2}, " +
		"scan_detail: {total_process_keys: 0, total_process_keys_size: 0, total_keys: 15, rocksdb: {delete_skipped_count: 5, key_skipped_count: 0, block: {cache_hit_count: 10, read_count: 0, read_byte: 100 Bytes}}}"
	require.Equal(t, str, cop.String())

	zeroScanDetail := util.ScanDetail{}
	require.Equal(t, "", zeroScanDetail.String())
}

func TestCopRuntimeStatsForTiFlash(t *testing.T) {
	stats := NewRuntimeStatsColl(nil)
	tableScanID := 1
	aggID := 2
	tableReaderID := 3
	stats.RecordOneCopTask(aggID, "tiflash", "8.8.8.8", mockExecutorExecutionSummaryForTiFlash(1, 1, 1, 1, "tablescan_"+strconv.Itoa(tableScanID)))
	stats.RecordOneCopTask(aggID, "tiflash", "8.8.8.9", mockExecutorExecutionSummaryForTiFlash(2, 2, 2, 1, "tablescan_"+strconv.Itoa(tableScanID)))
	stats.RecordOneCopTask(tableScanID, "tiflash", "8.8.8.8", mockExecutorExecutionSummaryForTiFlash(3, 3, 3, 1, "aggregation_"+strconv.Itoa(aggID)))
	stats.RecordOneCopTask(tableScanID, "tiflash", "8.8.8.9", mockExecutorExecutionSummaryForTiFlash(4, 4, 4, 1, "aggregation_"+strconv.Itoa(aggID)))
	scanDetail := &util.ScanDetail{
		TotalKeys:                 10,
		ProcessedKeys:             10,
		RocksdbDeleteSkippedCount: 10,
		RocksdbKeySkippedCount:    1,
		RocksdbBlockCacheHitCount: 10,
		RocksdbBlockReadCount:     10,
		RocksdbBlockReadByte:      100,
	}
	stats.RecordScanDetail(tableScanID, "tiflash", scanDetail)
	require.True(t, stats.ExistsCopStats(tableScanID))

	cop := stats.GetOrCreateCopStats(tableScanID, "tiflash")
	require.Equal(t, "tiflash_task:{proc max:2ns, min:1ns, avg: 1ns, p80:2ns, p95:2ns, iters:3, tasks:2, threads:2}", cop.String())

	copStats := cop.stats["8.8.8.8"]
	require.NotNil(t, copStats)

	copStats[0].SetRowNum(10)
	copStats[0].Record(time.Second, 10)
	require.Equal(t, "time:1s, loops:2, threads:1", copStats[0].String())
	expected := "tiflash_task:{proc max:4ns, min:3ns, avg: 3ns, p80:4ns, p95:4ns, iters:7, tasks:2, threads:2}"
	require.Equal(t, expected, stats.GetOrCreateCopStats(aggID, "tiflash").String())

	rootStats := stats.GetRootStats(tableReaderID)
	require.NotNil(t, rootStats)
	require.True(t, stats.ExistsRootStats(tableReaderID))
}

func TestRuntimeStatsWithCommit(t *testing.T) {
	commitDetail := &util.CommitDetails{
		GetCommitTsTime: time.Second,
		PrewriteTime:    time.Second,
		CommitTime:      time.Second,
		Mu: struct {
			sync.Mutex
			CommitBackoffTime int64
			BackoffTypes      []string
		}{
			CommitBackoffTime: int64(time.Second),
			BackoffTypes:      []string{"backoff1", "backoff2", "backoff1"},
		},
		WriteKeys:         3,
		WriteSize:         66,
		PrewriteRegionNum: 5,
		TxnRetry:          2,
		ResolveLock: util.ResolveLockDetail{
			ResolveLockTime: int64(time.Second),
		},
	}
	stats := &RuntimeStatsWithCommit{
		Commit: commitDetail,
	}
	expect := "commit_txn: {prewrite:1s, get_commit_ts:1s, commit:1s, backoff: {time: 1s, type: [backoff1 backoff2]}, resolve_lock: 1s, region_num:5, write_keys:3, write_byte:66, txn_retry:2}"
	require.Equal(t, expect, stats.String())

	lockDetail := &util.LockKeysDetails{
		TotalTime:   time.Second,
		RegionNum:   2,
		LockKeys:    10,
		BackoffTime: int64(time.Second * 3),
		Mu: struct {
			sync.Mutex
			BackoffTypes []string
		}{BackoffTypes: []string{
			"backoff4",
			"backoff5",
			"backoff5",
		}},
		LockRPCTime:  int64(time.Second * 5),
		LockRPCCount: 50,
		RetryCount:   2,
		ResolveLock: util.ResolveLockDetail{
			ResolveLockTime: int64(time.Second * 2),
		},
	}
	stats = &RuntimeStatsWithCommit{
		LockKeys: lockDetail,
	}
	expect = "lock_keys: {time:1s, region:2, keys:10, resolve_lock:2s, backoff: {time: 3s, type: [backoff4 backoff5]}, lock_rpc:5s, rpc_count:50, retry_count:2}"
	require.Equal(t, expect, stats.String())
}

func TestRootRuntimeStats(t *testing.T) {
	basic1 := &BasicRuntimeStats{}
	basic2 := &BasicRuntimeStats{}
	basic1.Record(time.Second, 20)
	basic2.Record(time.Second*2, 30)
	pid := 1
	stmtStats := NewRuntimeStatsColl(nil)
	stmtStats.RegisterStats(pid, basic1)
	stmtStats.RegisterStats(pid, basic2)
	concurrency := &RuntimeStatsWithConcurrencyInfo{}
	concurrency.SetConcurrencyInfo(NewConcurrencyInfo("worker", 15))
	stmtStats.RegisterStats(pid, concurrency)
	commitDetail := &util.CommitDetails{
		GetCommitTsTime:   time.Second,
		PrewriteTime:      time.Second,
		CommitTime:        time.Second,
		WriteKeys:         3,
		WriteSize:         66,
		PrewriteRegionNum: 5,
		TxnRetry:          2,
	}
	stmtStats.RegisterStats(pid, &RuntimeStatsWithCommit{
		Commit: commitDetail,
	})
	stats := stmtStats.GetRootStats(1)
	expect := "time:3s, loops:2, worker:15, commit_txn: {prewrite:1s, get_commit_ts:1s, commit:1s, region_num:5, write_keys:3, write_byte:66, txn_retry:2}"
	require.Equal(t, expect, stats.String())
}

func TestFormatDurationForExplain(t *testing.T) {
	cases := []struct {
		t string
		s string
	}{
		{"0s", "0s"},
		{"1ns", "1ns"},
		{"9ns", "9ns"},
		{"10ns", "10ns"},
		{"999ns", "999ns"},
		{"1µs", "1µs"},
		{"1.123µs", "1.12µs"},
		{"1.023µs", "1.02µs"},
		{"1.003µs", "1µs"},
		{"10.456µs", "10.5µs"},
		{"10.956µs", "11µs"},
		{"999.056µs", "999.1µs"},
		{"999.988µs", "1ms"},
		{"1.123ms", "1.12ms"},
		{"1.023ms", "1.02ms"},
		{"1.003ms", "1ms"},
		{"10.456ms", "10.5ms"},
		{"10.956ms", "11ms"},
		{"999.056ms", "999.1ms"},
		{"999.988ms", "1s"},
		{"1.123s", "1.12s"},
		{"1.023s", "1.02s"},
		{"1.003s", "1s"},
		{"10.456s", "10.5s"},
		{"10.956s", "11s"},
		{"16m39.056s", "16m39.1s"},
		{"16m39.988s", "16m40s"},
		{"24h16m39.388662s", "24h16m39.4s"},
		{"9.412345ms", "9.41ms"},
		{"10.412345ms", "10.4ms"},
		{"5.999s", "6s"},
		{"100.45µs", "100.5µs"},
	}
	for _, ca := range cases {
		d, err := time.ParseDuration(ca.t)
		require.NoError(t, err)

		result := FormatDuration(d)
		require.Equal(t, ca.s, result)
	}
}
