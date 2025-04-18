// Copyright 2022 PingCAP, Inc.
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

package schematracker

import (
	"bytes"
	"context"
	"fmt"
	"time"

	"github.com/ngaut/pools"
	"github.com/pingcap/tidb/ddl"
	"github.com/pingcap/tidb/ddl/util"
	"github.com/pingcap/tidb/executor"
	"github.com/pingcap/tidb/infoschema"
	"github.com/pingcap/tidb/kv"
	"github.com/pingcap/tidb/owner"
	"github.com/pingcap/tidb/parser/ast"
	"github.com/pingcap/tidb/parser/model"
	"github.com/pingcap/tidb/sessionctx"
	"github.com/pingcap/tidb/sessionctx/variable"
	"github.com/pingcap/tidb/statistics/handle"
	"github.com/pingcap/tidb/table"
	pumpcli "github.com/pingcap/tidb/tidb-binlog/pump_client"
)

// Checker is used to check the result of SchemaTracker is same as real DDL.
type Checker struct {
	realDDL ddl.DDL
	tracker SchemaTracker

	closed bool
}

// NewChecker creates a Checker.
func NewChecker(realDDL ddl.DDL) *Checker {
	return &Checker{
		realDDL: realDDL,
		tracker: NewSchemaTracker(2),
	}
}

// Disable turns off check.
func (d *Checker) Disable() {
	d.closed = true
}

// Enable turns on check.
func (d *Checker) Enable() {
	d.closed = false
}

func (d Checker) checkDBInfo(ctx sessionctx.Context, dbName model.CIStr) {
	if d.closed {
		return
	}
	dbInfo, _ := d.realDDL.GetInfoSchemaWithInterceptor(ctx).SchemaByName(dbName)
	dbInfo2 := d.tracker.SchemaByName(dbName)

	result := bytes.NewBuffer(make([]byte, 0, 512))
	err := executor.ConstructResultOfShowCreateDatabase(ctx, dbInfo, false, result)
	if err != nil {
		panic(err)
	}
	result2 := bytes.NewBuffer(make([]byte, 0, 512))
	err = executor.ConstructResultOfShowCreateDatabase(ctx, dbInfo2, false, result2)
	if err != nil {
		panic(err)
	}
	s1 := result.String()
	s2 := result2.String()
	if s1 != s2 {
		errStr := fmt.Sprintf("%s != %s", s1, s2)
		panic(errStr)
	}
}

// CreateSchema implements the DDL interface.
func (d Checker) CreateSchema(ctx sessionctx.Context, stmt *ast.CreateDatabaseStmt) error {
	err := d.realDDL.CreateSchema(ctx, stmt)
	if err != nil {
		return err
	}
	err = d.tracker.CreateSchema(ctx, stmt)
	if err != nil {
		panic(err)
	}

	d.checkDBInfo(ctx, stmt.Name)
	return nil
}

// AlterSchema implements the DDL interface.
func (d Checker) AlterSchema(sctx sessionctx.Context, stmt *ast.AlterDatabaseStmt) error {
	err := d.realDDL.AlterSchema(sctx, stmt)
	if err != nil {
		return err
	}
	err = d.tracker.AlterSchema(sctx, stmt)
	if err != nil {
		panic(err)
	}

	d.checkDBInfo(sctx, stmt.Name)
	return nil
}

// DropSchema implements the DDL interface.
func (d Checker) DropSchema(ctx sessionctx.Context, stmt *ast.DropDatabaseStmt) error {
	err := d.realDDL.DropSchema(ctx, stmt)
	if err != nil {
		return err
	}
	err = d.tracker.DropSchema(ctx, stmt)
	if err != nil {
		panic(err)
	}
	return nil
}

// CreateTable implements the DDL interface.
func (d Checker) CreateTable(ctx sessionctx.Context, stmt *ast.CreateTableStmt) error {
	//TODO implement me
	panic("implement me")
}

// CreateView implements the DDL interface.
func (d Checker) CreateView(ctx sessionctx.Context, stmt *ast.CreateViewStmt) error {
	//TODO implement me
	panic("implement me")
}

// DropTable implements the DDL interface.
func (d Checker) DropTable(ctx sessionctx.Context, stmt *ast.DropTableStmt) (err error) {
	//TODO implement me
	panic("implement me")
}

// RecoverTable implements the DDL interface.
func (d Checker) RecoverTable(ctx sessionctx.Context, recoverInfo *ddl.RecoverInfo) (err error) {
	//TODO implement me
	panic("implement me")
}

// DropView implements the DDL interface.
func (d Checker) DropView(ctx sessionctx.Context, stmt *ast.DropTableStmt) (err error) {
	//TODO implement me
	panic("implement me")
}

// CreateIndex implements the DDL interface.
func (d Checker) CreateIndex(ctx sessionctx.Context, stmt *ast.CreateIndexStmt) error {
	//TODO implement me
	panic("implement me")
}

// DropIndex implements the DDL interface.
func (d Checker) DropIndex(ctx sessionctx.Context, stmt *ast.DropIndexStmt) error {
	//TODO implement me
	panic("implement me")
}

// AlterTable implements the DDL interface.
func (d Checker) AlterTable(ctx context.Context, sctx sessionctx.Context, stmt *ast.AlterTableStmt) error {
	//TODO implement me
	panic("implement me")
}

// TruncateTable implements the DDL interface.
func (d Checker) TruncateTable(ctx sessionctx.Context, tableIdent ast.Ident) error {
	//TODO implement me
	panic("implement me")
}

// RenameTable implements the DDL interface.
func (d Checker) RenameTable(ctx sessionctx.Context, stmt *ast.RenameTableStmt) error {
	//TODO implement me
	panic("implement me")
}

// LockTables implements the DDL interface.
func (d Checker) LockTables(ctx sessionctx.Context, stmt *ast.LockTablesStmt) error {
	//TODO implement me
	panic("implement me")
}

// UnlockTables implements the DDL interface.
func (d Checker) UnlockTables(ctx sessionctx.Context, lockedTables []model.TableLockTpInfo) error {
	//TODO implement me
	panic("implement me")
}

// CleanupTableLock implements the DDL interface.
func (d Checker) CleanupTableLock(ctx sessionctx.Context, tables []*ast.TableName) error {
	//TODO implement me
	panic("implement me")
}

// UpdateTableReplicaInfo implements the DDL interface.
func (d Checker) UpdateTableReplicaInfo(ctx sessionctx.Context, physicalID int64, available bool) error {
	//TODO implement me
	panic("implement me")
}

// RepairTable implements the DDL interface.
func (d Checker) RepairTable(ctx sessionctx.Context, table *ast.TableName, createStmt *ast.CreateTableStmt) error {
	//TODO implement me
	panic("implement me")
}

// CreateSequence implements the DDL interface.
func (d Checker) CreateSequence(ctx sessionctx.Context, stmt *ast.CreateSequenceStmt) error {
	//TODO implement me
	panic("implement me")
}

// DropSequence implements the DDL interface.
func (d Checker) DropSequence(ctx sessionctx.Context, stmt *ast.DropSequenceStmt) (err error) {
	//TODO implement me
	panic("implement me")
}

// AlterSequence implements the DDL interface.
func (d Checker) AlterSequence(ctx sessionctx.Context, stmt *ast.AlterSequenceStmt) error {
	//TODO implement me
	panic("implement me")
}

// CreatePlacementPolicy implements the DDL interface.
func (d Checker) CreatePlacementPolicy(ctx sessionctx.Context, stmt *ast.CreatePlacementPolicyStmt) error {
	//TODO implement me
	panic("implement me")
}

// DropPlacementPolicy implements the DDL interface.
func (d Checker) DropPlacementPolicy(ctx sessionctx.Context, stmt *ast.DropPlacementPolicyStmt) error {
	//TODO implement me
	panic("implement me")
}

// AlterPlacementPolicy implements the DDL interface.
func (d Checker) AlterPlacementPolicy(ctx sessionctx.Context, stmt *ast.AlterPlacementPolicyStmt) error {
	//TODO implement me
	panic("implement me")
}

// CreateSchemaWithInfo implements the DDL interface.
func (d Checker) CreateSchemaWithInfo(ctx sessionctx.Context, info *model.DBInfo, onExist ddl.OnExist) error {
	err := d.realDDL.CreateSchemaWithInfo(ctx, info, onExist)
	if err != nil {
		return err
	}
	err = d.tracker.CreateSchemaWithInfo(ctx, info, onExist)
	if err != nil {
		panic(err)
	}

	d.checkDBInfo(ctx, info.Name)
	return nil
}

// CreateTableWithInfo implements the DDL interface.
func (d Checker) CreateTableWithInfo(ctx sessionctx.Context, schema model.CIStr, info *model.TableInfo, onExist ddl.OnExist) error {
	//TODO implement me
	panic("implement me")
}

// BatchCreateTableWithInfo implements the DDL interface.
func (d Checker) BatchCreateTableWithInfo(ctx sessionctx.Context, schema model.CIStr, info []*model.TableInfo, onExist ddl.OnExist) error {
	//TODO implement me
	panic("implement me")
}

// CreatePlacementPolicyWithInfo implements the DDL interface.
func (d Checker) CreatePlacementPolicyWithInfo(ctx sessionctx.Context, policy *model.PolicyInfo, onExist ddl.OnExist) error {
	//TODO implement me
	panic("implement me")
}

// Start implements the DDL interface.
func (d Checker) Start(ctxPool *pools.ResourcePool) error {
	return d.realDDL.Start(ctxPool)
}

// GetLease implements the DDL interface.
func (d Checker) GetLease() time.Duration {
	return d.realDDL.GetLease()
}

// Stats implements the DDL interface.
func (d Checker) Stats(vars *variable.SessionVars) (map[string]interface{}, error) {
	return d.realDDL.Stats(vars)
}

// GetScope implements the DDL interface.
func (d Checker) GetScope(status string) variable.ScopeFlag {
	return d.realDDL.GetScope(status)
}

// Stop implements the DDL interface.
func (d Checker) Stop() error {
	return d.realDDL.Stop()
}

// RegisterStatsHandle implements the DDL interface.
func (d Checker) RegisterStatsHandle(h *handle.Handle) {
	d.realDDL.RegisterStatsHandle(h)
}

// SchemaSyncer implements the DDL interface.
func (d Checker) SchemaSyncer() util.SchemaSyncer {
	return d.realDDL.SchemaSyncer()
}

// OwnerManager implements the DDL interface.
func (d Checker) OwnerManager() owner.Manager {
	return d.realDDL.OwnerManager()
}

// GetID implements the DDL interface.
func (d Checker) GetID() string {
	return d.realDDL.GetID()
}

// GetTableMaxHandle implements the DDL interface.
func (d Checker) GetTableMaxHandle(ctx *ddl.JobContext, startTS uint64, tbl table.PhysicalTable) (kv.Handle, bool, error) {
	return d.realDDL.GetTableMaxHandle(ctx, startTS, tbl)
}

// SetBinlogClient implements the DDL interface.
func (d Checker) SetBinlogClient(client *pumpcli.PumpsClient) {
	d.realDDL.SetBinlogClient(client)
}

// GetHook implements the DDL interface.
func (d Checker) GetHook() ddl.Callback {
	return d.realDDL.GetHook()
}

// SetHook implements the DDL interface.
func (d Checker) SetHook(h ddl.Callback) {
	d.realDDL.SetHook(h)
}

// GetInfoSchemaWithInterceptor implements the DDL interface.
func (d Checker) GetInfoSchemaWithInterceptor(ctx sessionctx.Context) infoschema.InfoSchema {
	return d.realDDL.GetInfoSchemaWithInterceptor(ctx)
}

// DoDDLJob implements the DDL interface.
func (d Checker) DoDDLJob(ctx sessionctx.Context, job *model.Job) error {
	return d.realDDL.DoDDLJob(ctx, job)
}
