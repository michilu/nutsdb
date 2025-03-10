// Copyright 2019 The nutsdb Author. All rights reserved.
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

package nutsdb

import (
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path"
	"sort"
	"strings"
	"sync"

	"github.com/xujiajun/nutsdb/ds/list"
	"github.com/xujiajun/nutsdb/ds/set"
	"github.com/xujiajun/nutsdb/ds/zset"
	"github.com/xujiajun/utils/filesystem"
	"github.com/xujiajun/utils/strconv2"
)

var (
	// ErrDBClosed is returned when db is closed.
	ErrDBClosed = errors.New("db is closed")

	// ErrBucket is returned when bucket is not in the HintIdx.
	ErrBucket = errors.New("err bucket")

	// ErrEntryIdxModeOpt is returned when set db EntryIdxMode option is wrong.
	ErrEntryIdxModeOpt = errors.New("err EntryIdxMode option set")

	// ErrFn is returned when fn is nil.
	ErrFn = errors.New("err fn")
)

const (
	// DataDeleteFlag represents the data delete flag
	DataDeleteFlag uint16 = iota

	// DataSetFlag represents the data set flag
	DataSetFlag

	// DataLPushFlag represents the data LPush flag
	DataLPushFlag

	// DataRPushFlag represents the data RPush flag
	DataRPushFlag

	// DataLRemFlag represents the data LRem flag
	DataLRemFlag

	// DataLPopFlag represents the data LPop flag
	DataLPopFlag

	// DataRPopFlag represents the data RPop flag
	DataRPopFlag

	// DataLSetFlag represents the data LSet flag
	DataLSetFlag

	// DataLTrimFlag represents the data LTrim flag
	DataLTrimFlag

	// DataZAddFlag represents the data ZAdd flag
	DataZAddFlag

	// DataZRemFlag represents the data ZRem flag
	DataZRemFlag

	// DataZRemRangeByRankFlag represents the data ZRemRangeByRank flag
	DataZRemRangeByRankFlag

	// DataZPopMaxFlag represents the data ZPopMax flag
	DataZPopMaxFlag

	// DataZPopMinFlag represents the data aZPopMin flag
	DataZPopMinFlag
)

const (
	// UnCommitted represents the tx unCommitted status
	UnCommitted uint16 = 0

	// Committed represents the tx committed status
	Committed uint16 = 1

	// Persistent represents the data persistent flag
	Persistent uint32 = 0

	// ScanNoLimit represents the data scan no limit flag
	ScanNoLimit int = -1
)

const (
	// DataStructureSet represents the data structure set flag
	DataStructureSet uint16 = iota

	// DataStructureSortedSet represents the data structure sorted set flag
	DataStructureSortedSet

	// DataStructureBPTree represents the data structure b+ tree flag
	DataStructureBPTree

	// DataStructureList represents the data structure list flag
	DataStructureList
)

type (
	// DB represents a collection of buckets that persist on disk.
	DB struct {
		opt                     Options   // the database options
		BPTreeIdx               BPTreeIdx // Hint Index
		BPTreeRootIdxes         []*BPTreeRootIdx
		BPTreeKeyEntryPosMap    map[string]int64 // key = bucket+key  val = EntryPos
		SetIdx                  SetIdx
		SortedSetIdx            SortedSetIdx
		ListIdx                 ListIdx
		ActiveFile              *DataFile
		ActiveBPTreeIdx         *BPTree
		ActiveCommittedTxIdsIdx *BPTree
		committedTxIds          map[uint64]struct{}
		MaxFileID               int64
		mu                      sync.RWMutex
		KeyCount                int // total key number ,include expired, deleted, repeated.
		closed                  bool
		isMerging               bool
	}

	// BPTreeIdx represents the B+ tree index
	BPTreeIdx map[string]*BPTree

	// SetIdx represents the set index
	SetIdx map[string]*set.Set

	// SortedSetIdx represents the sorted set index
	SortedSetIdx map[string]*zset.SortedSet

	// ListIdx represents the list index
	ListIdx map[string]*list.List

	// Entries represents entries
	Entries []*Entry
)

// Open returns a newly initialized DB object.
func Open(opt Options) (*DB, error) {
	db := &DB{
		BPTreeIdx:               make(BPTreeIdx),
		SetIdx:                  make(SetIdx),
		SortedSetIdx:            make(SortedSetIdx),
		ListIdx:                 make(ListIdx),
		ActiveBPTreeIdx:         NewTree(),
		MaxFileID:               0,
		opt:                     opt,
		KeyCount:                0,
		closed:                  false,
		committedTxIds:          make(map[uint64]struct{}),
		BPTreeKeyEntryPosMap:    make(map[string]int64),
		ActiveCommittedTxIdsIdx: NewTree(),
	}

	if ok := filesystem.PathIsExist(db.opt.Dir); !ok {
		if err := os.MkdirAll(db.opt.Dir, os.ModePerm); err != nil {
			return nil, err
		}
	}

	if err := db.checkEntryIdxMode(); err != nil {
		return nil, err
	}

	if opt.EntryIdxMode == HintBPTSparseIdxMode {
		bptRootIdxDir := db.opt.Dir + "/" + bptDir + "/root"
		if ok := filesystem.PathIsExist(bptRootIdxDir); !ok {
			if err := os.MkdirAll(bptRootIdxDir, os.ModePerm); err != nil {
				return nil, err
			}
		}

		bptTxIDIdxDir := db.opt.Dir + "/" + bptDir + "/txid"
		if ok := filesystem.PathIsExist(bptTxIDIdxDir); !ok {
			if err := os.MkdirAll(bptTxIDIdxDir, os.ModePerm); err != nil {
				return nil, err
			}
		}
	}

	if err := db.buildIndexes(); err != nil {
		return nil, fmt.Errorf("db.buildIndexes error: %s", err)
	}

	return db, nil
}

func (db *DB) checkEntryIdxMode() error {
	hasDataFlag := false
	hasBptDirFlag := false

	files, err := ioutil.ReadDir(db.opt.Dir)
	if err != nil {
		return err
	}

	for _, f := range files {
		id := f.Name()

		fileSuffix := path.Ext(path.Base(id))
		if fileSuffix == DataSuffix {
			hasDataFlag = true
			if hasBptDirFlag {
				break
			}
		}

		if id == bptDir {
			hasBptDirFlag = true
		}
	}

	if db.opt.EntryIdxMode != HintBPTSparseIdxMode && hasDataFlag && hasBptDirFlag {
		return errors.New("not support HintBPTSparseIdxMode switch to the other EntryIdxMode")
	}

	if db.opt.EntryIdxMode == HintBPTSparseIdxMode && hasBptDirFlag == false && hasDataFlag == true {
		return errors.New("not support the other EntryIdxMode switch to HintBPTSparseIdxMode")
	}

	return nil
}

// Update executes a function within a managed read/write transaction.
func (db *DB) Update(fn func(tx *Tx) error) error {
	if fn == nil {
		return ErrFn
	}

	return db.managed(true, fn)
}

// View executes a function within a managed read-only transaction.
func (db *DB) View(fn func(tx *Tx) error) error {
	if fn == nil {
		return ErrFn
	}

	return db.managed(false, fn)
}

// Merge removes dirty data and reduce data redundancy,following these steps:
//
// 1. Filter delete or expired entry.
//
// 2. Write entry to activeFile if the key not exist，if exist miss this write operation.
//
// 3. Filter the entry which is committed.
//
// 4. At last remove the merged files.
//
// Caveat: Merge is Called means starting multiple write transactions, and it
// will effect the other write request. so execute it at the appropriate time.
func (db *DB) Merge() error {
	var (
		off                 int64
		pendingMergeFIds    []int
		pendingMergeEntries []*Entry
	)

	if db.opt.EntryIdxMode == HintBPTSparseIdxMode {
		return errors.New("not support mode `HintBPTSparseIdxMode`")
	}

	db.isMerging = true

	_, pendingMergeFIds = db.getMaxFileIDAndFileIDs()

	if len(pendingMergeFIds) < 2 {
		db.isMerging = false
		return errors.New("the number of files waiting to be merged is at least 2")
	}

	for _, pendingMergeFId := range pendingMergeFIds {
		off = 0
		f, err := NewDataFile(db.getDataPath(int64(pendingMergeFId)), db.opt.SegmentSize, db.opt.RWMode)
		if err != nil {
			db.isMerging = false
			return err
		}

		pendingMergeEntries = []*Entry{}

		for {
			if entry, err := f.ReadAt(int(off)); err == nil {
				if entry == nil {
					break
				}

				if db.isFilterEntry(entry) {
					off += entry.Size()
					if off >= db.opt.SegmentSize {
						break
					}
					continue
				}

				pendingMergeEntries = db.getPendingMergeEntries(entry, pendingMergeEntries)

				off += entry.Size()
				if off >= db.opt.SegmentSize {
					break
				}

			} else {
				if err == io.EOF {
					break
				}
				f.rwManager.Close()
				return fmt.Errorf("when merge operation build hintIndex readAt err: %s", err)
			}
		}

		if err := db.reWriteData(pendingMergeEntries); err != nil {
			f.rwManager.Close()
			return err
		}

		if err := os.Remove(db.getDataPath(int64(pendingMergeFId))); err != nil {
			db.isMerging = false
			f.rwManager.Close()
			return fmt.Errorf("when merge err: %s", err)
		}

		f.rwManager.Close()
	}

	return nil
}

// Backup copies the database to file directory at the given dir.
func (db *DB) Backup(dir string) error {
	err := db.View(func(tx *Tx) error {
		return filesystem.CopyDir(db.opt.Dir, dir)
	})
	if err != nil {
		return err
	}

	return nil
}

// Close releases all db resources.
func (db *DB) Close() error {
	db.mu.Lock()
	defer db.mu.Unlock()

	if db.closed {
		return ErrDBClosed
	}

	db.closed = true

	db.ActiveFile.rwManager.Close()

	db.ActiveFile = nil

	db.BPTreeIdx = nil

	return nil
}

// setActiveFile sets the ActiveFile (DataFile object).
func (db *DB) setActiveFile() (err error) {
	filepath := db.getDataPath(db.MaxFileID)
	db.ActiveFile, err = NewDataFile(filepath, db.opt.SegmentSize, db.opt.RWMode)
	if err != nil {
		return
	}

	db.ActiveFile.fileID = db.MaxFileID

	return nil
}

// getMaxFileIDAndFileIds returns max fileId and fileIds.
func (db *DB) getMaxFileIDAndFileIDs() (maxFileID int64, dataFileIds []int) {
	files, _ := ioutil.ReadDir(db.opt.Dir)
	if len(files) == 0 {
		return 0, nil
	}

	maxFileID = 0

	for _, f := range files {
		id := f.Name()
		fileSuffix := path.Ext(path.Base(id))
		if fileSuffix != DataSuffix {
			continue
		}

		id = strings.TrimSuffix(id, DataSuffix)
		idVal, _ := strconv2.StrToInt(id)
		dataFileIds = append(dataFileIds, idVal)
	}

	if len(dataFileIds) == 0 {
		return 0, nil
	}

	sort.Ints(dataFileIds)
	maxFileID = int64(dataFileIds[len(dataFileIds)-1])

	return
}

// getActiveFileWriteOff returns the write offset of activeFile.
func (db *DB) getActiveFileWriteOff() (off int64, err error) {
	off = 0
	for {
		if item, err := db.ActiveFile.ReadAt(int(off)); err == nil {
			if item == nil {
				break
			}

			off += item.Size()
			//set ActiveFileActualSize
			db.ActiveFile.ActualSize = off

		} else {
			if err == io.EOF {
				break
			}

			return -1, fmt.Errorf("when build activeDataIndex readAt err: %s", err)
		}
	}

	return
}

func (db *DB) parseDataFiles(dataFileIds []int) (unconfirmedRecords []*Record, committedTxIds map[uint64]struct{}, err error) {
	var (
		off int64
		e   *Entry
	)

	committedTxIds = make(map[uint64]struct{})

	if db.opt.EntryIdxMode == HintBPTSparseIdxMode {
		dataFileIds = dataFileIds[len(dataFileIds)-1:]
	}

	for _, dataID := range dataFileIds {
		off = 0
		fID := int64(dataID)
		f, err := NewDataFile(db.getDataPath(fID), db.opt.SegmentSize, db.opt.StartFileLoadingMode)
		if err != nil {
			return nil, nil, err
		}

		for {
			if entry, err := f.ReadAt(int(off)); err == nil {
				if entry == nil {
					break
				}

				e = nil
				if db.opt.EntryIdxMode == HintKeyValAndRAMIdxMode {
					e = &Entry{
						Key:   entry.Key,
						Value: entry.Value,
						Meta:  entry.Meta,
					}
				}

				if entry.Meta.status == Committed {
					committedTxIds[entry.Meta.txID] = struct{}{}
					db.ActiveCommittedTxIdsIdx.Insert([]byte(strconv2.Int64ToStr(int64(entry.Meta.txID))), nil,
						&Hint{meta: &MetaData{Flag: DataSetFlag}}, CountFlagEnabled)
				}

				unconfirmedRecords = append(unconfirmedRecords, &Record{
					H: &Hint{
						key:     entry.Key,
						fileID:  fID,
						meta:    entry.Meta,
						dataPos: uint64(off),
					},
					E: e,
				})

				if db.opt.EntryIdxMode == HintBPTSparseIdxMode {
					db.BPTreeKeyEntryPosMap[string(entry.Meta.bucket)+string(entry.Key)] = off
				}

				off += entry.Size()

			} else {
				if err == io.EOF {
					break
				}

				if off >= db.opt.SegmentSize {
					break
				}
				f.rwManager.Close()
				return nil, nil, fmt.Errorf("when build hintIndex readAt err: %s", err)
			}
		}

		f.rwManager.Close()
	}

	return
}

func (db *DB) buildBPTreeRootIdxes(dataFileIds []int) error {
	var off int64

	dataFileIdsSize := len(dataFileIds)

	if dataFileIdsSize == 1 {
		return nil
	}

	for i := 0; i < len(dataFileIds[0:dataFileIdsSize-1]); i++ {
		fID := dataFileIds[i]
		off = 0
		for {
			bs, err := ReadBPTreeRootIdxAt(db.getBPTRootPath(int64(fID)), off)
			if err == io.EOF || err == nil && bs == nil {
				break
			}
			if err != nil {
				return err
			}

			if err == nil && bs != nil {
				db.BPTreeRootIdxes = append(db.BPTreeRootIdxes, bs)
				off += bs.Size()
			}

		}
	}

	db.committedTxIds = nil

	return nil
}

func (db *DB) buildBPTreeIdx(bucket string, r *Record) error {
	if _, ok := db.BPTreeIdx[bucket]; !ok {
		db.BPTreeIdx[bucket] = NewTree()
	}

	if err := db.BPTreeIdx[bucket].Insert(r.H.key, r.E, r.H, CountFlagEnabled); err != nil {
		return fmt.Errorf("when build BPTreeIdx insert index err: %s", err)
	}

	return nil
}

func (db *DB) buildActiveBPTreeIdx(r *Record) error {
	newKey := r.H.meta.bucket
	newKey = append(newKey, r.H.key...)

	if err := db.ActiveBPTreeIdx.Insert(newKey, r.E, r.H, CountFlagEnabled); err != nil {
		return fmt.Errorf("when build BPTreeIdx insert index err: %s", err)
	}

	return nil
}

func (db *DB) buildOtherIdxes(bucket string, r *Record) error {
	if r.H.meta.ds == DataStructureSet {
		if err := db.buildSetIdx(bucket, r); err != nil {
			return err
		}
	}

	if r.H.meta.ds == DataStructureSortedSet {
		if err := db.buildSortedSetIdx(bucket, r); err != nil {
			return err
		}
	}

	if r.H.meta.ds == DataStructureList {
		if err := db.buildListIdx(bucket, r); err != nil {
			return err
		}
	}

	return nil
}

// buildHintIdx builds the Hint Indexes.
func (db *DB) buildHintIdx(dataFileIds []int) error {
	unconfirmedRecords, committedTxIds, err := db.parseDataFiles(dataFileIds)
	db.committedTxIds = committedTxIds

	if err != nil {
		return err
	}

	if len(unconfirmedRecords) == 0 {
		return nil
	}

	for _, r := range unconfirmedRecords {
		if _, ok := db.committedTxIds[r.H.meta.txID]; ok {
			bucket := string(r.H.meta.bucket)

			if r.H.meta.ds == DataStructureBPTree {
				r.H.meta.status = Committed

				if db.opt.EntryIdxMode == HintBPTSparseIdxMode {
					if err = db.buildActiveBPTreeIdx(r); err != nil {
						return err
					}
				} else {
					if err = db.buildBPTreeIdx(bucket, r); err != nil {
						return err
					}
				}
			}

			if err = db.buildOtherIdxes(bucket, r); err != nil {
				return err
			}

			db.KeyCount++
		}
	}

	if HintBPTSparseIdxMode == db.opt.EntryIdxMode {
		if err = db.buildBPTreeRootIdxes(dataFileIds); err != nil {
			return err
		}
	}

	return nil
}

// buildSetIdx builds set index when opening the DB.
func (db *DB) buildSetIdx(bucket string, r *Record) error {
	if _, ok := db.SetIdx[bucket]; !ok {
		db.SetIdx[bucket] = set.New()
	}

	if r.E == nil {
		return ErrEntryIdxModeOpt
	}

	if r.H.meta.Flag == DataSetFlag {
		if err := db.SetIdx[bucket].SAdd(string(r.E.Key), r.E.Value); err != nil {
			return fmt.Errorf("when build SetIdx SAdd index err: %s", err)
		}
	}

	if r.H.meta.Flag == DataDeleteFlag {
		if err := db.SetIdx[bucket].SRem(string(r.E.Key), r.E.Value); err != nil {
			return fmt.Errorf("when build SetIdx SRem index err: %s", err)
		}
	}

	return nil
}

// buildSortedSetIdx builds sorted set index when opening the DB.
func (db *DB) buildSortedSetIdx(bucket string, r *Record) error {
	if _, ok := db.SortedSetIdx[bucket]; !ok {
		db.SortedSetIdx[bucket] = zset.New()
	}

	if r.H.meta.Flag == DataZAddFlag {
		keyAndScore := strings.Split(string(r.E.Key), SeparatorForZSetKey)
		if len(keyAndScore) == 2 {
			key := keyAndScore[0]
			score, _ := strconv2.StrToFloat64(keyAndScore[1])
			if r.E == nil {
				return ErrEntryIdxModeOpt
			}
			_ = db.SortedSetIdx[bucket].Put(key, zset.SCORE(score), r.E.Value)
		}
	}
	if r.H.meta.Flag == DataZRemFlag {
		_ = db.SortedSetIdx[bucket].Remove(string(r.E.Key))
	}
	if r.H.meta.Flag == DataZRemRangeByRankFlag {
		start, _ := strconv2.StrToInt(string(r.E.Key))
		end, _ := strconv2.StrToInt(string(r.E.Value))
		_ = db.SortedSetIdx[bucket].GetByRankRange(start, end, true)
	}
	if r.H.meta.Flag == DataZPopMaxFlag {
		_ = db.SortedSetIdx[bucket].PopMax()
	}
	if r.H.meta.Flag == DataZPopMinFlag {
		_ = db.SortedSetIdx[bucket].PopMin()
	}

	return nil
}

//buildListIdx builds List index when opening the DB.
func (db *DB) buildListIdx(bucket string, r *Record) error {
	if _, ok := db.ListIdx[bucket]; !ok {
		db.ListIdx[bucket] = list.New()
	}

	if r.E == nil {
		return ErrEntryIdxModeOpt
	}

	switch r.H.meta.Flag {
	case DataLPushFlag:
		_, _ = db.ListIdx[bucket].LPush(string(r.E.Key), r.E.Value)
	case DataRPushFlag:
		_, _ = db.ListIdx[bucket].RPush(string(r.E.Key), r.E.Value)
	case DataLRemFlag:
		count, _ := strconv2.StrToInt(string(r.E.Value))
		if _, err := db.ListIdx[bucket].LRem(string(r.E.Key), count); err != nil {
			return ErrWhenBuildListIdx(err)
		}
	case DataLPopFlag:
		if _, err := db.ListIdx[bucket].LPop(string(r.E.Key)); err != nil {
			return ErrWhenBuildListIdx(err)
		}
	case DataRPopFlag:
		if _, err := db.ListIdx[bucket].RPop(string(r.E.Key)); err != nil {
			return ErrWhenBuildListIdx(err)
		}
	case DataLSetFlag:
		keyAndIndex := strings.Split(string(r.E.Key), SeparatorForListKey)
		newKey := keyAndIndex[0]
		index, _ := strconv2.StrToInt(keyAndIndex[1])
		if err := db.ListIdx[bucket].LSet(newKey, index, r.E.Value); err != nil {
			return ErrWhenBuildListIdx(err)
		}
	case DataLTrimFlag:
		keyAndStartIndex := strings.Split(string(r.E.Key), SeparatorForListKey)
		newKey := keyAndStartIndex[0]
		start, _ := strconv2.StrToInt(keyAndStartIndex[1])
		end, _ := strconv2.StrToInt(string(r.E.Value))
		if err := db.ListIdx[bucket].Ltrim(newKey, start, end); err != nil {
			return ErrWhenBuildListIdx(err)
		}
	}

	return nil
}

// ErrWhenBuildListIdx returns err when build listIdx
func ErrWhenBuildListIdx(err error) error {
	return fmt.Errorf("when build listIdx LRem err: %s", err)
}

// buildIndexes builds indexes when db initialize resource.
func (db *DB) buildIndexes() (err error) {
	var (
		maxFileID   int64
		dataFileIds []int
	)

	maxFileID, dataFileIds = db.getMaxFileIDAndFileIDs()

	//init db.ActiveFile
	db.MaxFileID = maxFileID

	//set ActiveFile
	if err = db.setActiveFile(); err != nil {
		return
	}

	if dataFileIds == nil && maxFileID == 0 {
		return
	}

	if db.ActiveFile.writeOff, err = db.getActiveFileWriteOff(); err != nil {
		return
	}

	// build hint index
	return db.buildHintIdx(dataFileIds)
}

// managed calls a block of code that is fully contained in a transaction.
func (db *DB) managed(writable bool, fn func(tx *Tx) error) error {
	var tx *Tx

	tx, err := db.Begin(writable)
	if err != nil {
		return err
	}

	if err = fn(tx); err != nil {
		if errRollback := tx.Rollback(); errRollback != nil {
			return errRollback
		}
		return err
	}

	if err = tx.Commit(); err != nil {
		if errRollback := tx.Rollback(); errRollback != nil {
			return errRollback
		}
		return err
	}

	return nil
}

const bptDir = "bpt"

// getDataPath returns the data path at given fid.
func (db *DB) getDataPath(fID int64) string {
	return db.opt.Dir + "/" + strconv2.Int64ToStr(fID) + DataSuffix
}

func (db *DB) getBPTDir() string {
	return db.opt.Dir + "/" + bptDir
}

func (db *DB) getBPTPath(fID int64) string {
	return db.getBPTDir() + "/" + strconv2.Int64ToStr(fID) + BPTIndexSuffix
}

func (db *DB) getBPTRootPath(fID int64) string {
	return db.getBPTDir() + "/root/" + strconv2.Int64ToStr(fID) + BPTRootIndexSuffix
}

func (db *DB) getBPTTxIdPath(fID int64) string {
	return db.getBPTDir() + "/txid/" + strconv2.Int64ToStr(fID) + BPTTxIdIndexSuffix
}

func (db *DB) getBPTRootTxIdPath(fID int64) string {
	return db.getBPTDir() + "/txid/" + strconv2.Int64ToStr(fID) + BPTRootTxIdIndexSuffix
}

func (db *DB) getPendingMergeEntries(entry *Entry, pendingMergeEntries []*Entry) []*Entry {
	if entry.Meta.ds == DataStructureBPTree {
		if r, err := db.BPTreeIdx[string(entry.Meta.bucket)].Find(entry.Key); err == nil {
			if r.H.meta.Flag == DataSetFlag {
				pendingMergeEntries = append(pendingMergeEntries, entry)
			}
		}
	}

	if entry.Meta.ds == DataStructureSet {
		if db.SetIdx[string(entry.Meta.bucket)].SIsMember(string(entry.Key), entry.Value) {
			pendingMergeEntries = append(pendingMergeEntries, entry)
		}
	}

	if entry.Meta.ds == DataStructureSortedSet {
		keyAndScore := strings.Split(string(entry.Key), SeparatorForZSetKey)
		if len(keyAndScore) == 2 {
			key := keyAndScore[0]
			n := db.SortedSetIdx[string(entry.Meta.bucket)].GetByKey(key)
			if n != nil {
				pendingMergeEntries = append(pendingMergeEntries, entry)
			}
		}
	}

	if entry.Meta.ds == DataStructureList {
		items, _ := db.ListIdx[string(entry.Meta.bucket)].LRange(string(entry.Key), 0, -1)
		ok := false
		if entry.Meta.Flag == DataRPushFlag || entry.Meta.Flag == DataLPushFlag {
			for _, item := range items {
				if string(entry.Value) == string(item) {
					ok = true
					break
				}
			}
			if ok {
				pendingMergeEntries = append(pendingMergeEntries, entry)
			}
		}
	}

	return pendingMergeEntries
}

func (db *DB) reWriteData(pendingMergeEntries []*Entry) error {
	tx, err := db.Begin(true)
	if err != nil {
		db.isMerging = false
		return err
	}

	dataFile, err := NewDataFile(db.getDataPath(db.MaxFileID+1), db.opt.SegmentSize, db.opt.RWMode)
	if err != nil {
		db.isMerging = false
		return err
	}
	db.ActiveFile = dataFile
	db.MaxFileID++

	for _, e := range pendingMergeEntries {
		err := tx.put(string(e.Meta.bucket), e.Key, e.Value, e.Meta.TTL, e.Meta.Flag, e.Meta.timestamp, e.Meta.ds)
		if err != nil {
			tx.Rollback()
			db.isMerging = false
			return err
		}
	}
	tx.Commit()
	return nil
}

func (db *DB) isFilterEntry(entry *Entry) bool {
	if entry.Meta.Flag == DataDeleteFlag || entry.Meta.Flag == DataRPopFlag ||
		entry.Meta.Flag == DataLPopFlag || entry.Meta.Flag == DataLRemFlag ||
		entry.Meta.Flag == DataLTrimFlag || entry.Meta.Flag == DataZRemFlag ||
		entry.Meta.Flag == DataZRemRangeByRankFlag || entry.Meta.Flag == DataZPopMaxFlag ||
		entry.Meta.Flag == DataZPopMinFlag || IsExpired(entry.Meta.TTL, entry.Meta.timestamp) {
		return true
	}

	return false
}
