package worker

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"fmt"
	"math"
	"os"
	"path"
	"strconv"
	"sync"
	"time"

	"github.com/dgraph-io/dgraph/posting"
	"github.com/dgraph-io/dgraph/posting/types"
	stype "github.com/dgraph-io/dgraph/types"
	"github.com/dgraph-io/dgraph/x"
)

const numBackupRoutines = 10

type kv struct {
	key, value []byte
}

// Backup creates a backup of data by exporting it as an RDF gzip.
func backup(gid uint32, bpath string) error {
	ch := make(chan []byte, 10000)
	chkv := make(chan kv, 1000)
	errChan := make(chan error, 1)
	it := pstore.NewIterator()
	defer it.Close()
	var wg sync.WaitGroup
	// var lastPred string

	wg.Add(numBackupRoutines)
	for i := 0; i < numBackupRoutines; i++ {
		go func() {
			for item := range chkv {
				toRDF(item, ch)
			}
			wg.Done()
		}()
	}
	go writeToFile(strconv.Itoa(int(gid)), bpath, ch, errChan)

	uidPrefix := []byte("_uid_")
	count := 0
	for it.SeekToFirst(); it.Valid(); {
		key := it.Key().Data()
		// cidx := bytes.IndexRune(key, ':')
		// if cidx > -1 {
		// 	// Seek to the end of index keys.
		// 	// NOTE: We can't directly assign pre to key, because key is pointing to unsafe C array.
		// 	pre := make([]byte, cidx+2)
		// 	copy(pre[0:cidx+1], key)
		// 	pre[cidx+1] = '~'
		// 	it.Seek(pre)
		// 	continue
		// }
		if bytes.HasPrefix(key, uidPrefix) {
			// Skip the UID mappings.
			it.Seek([]byte("_uid_~"))
			continue
		}
		pred, uid := posting.SplitKey(key)
		// if pred != lastPred && BelongsTo(pred) != gid {
		// 	it.Seek([]byte(fmt.Sprintf("%s~", pred)))
		// 	continue
		// }

		k := []byte(fmt.Sprintf("<_uid_:%#x> <%s> ", uid, pred))
		v := make([]byte, len(it.Value().Data()))
		count++
		fmt.Println(count)
		copy(v, it.Value().Data())
		chkv <- kv{
			key:   k,
			value: v,
		}
		// lastPred = pred
		it.Next()
	}

	close(chkv)
	// Wait for numBackupRoutines to finish.
	wg.Wait()
	close(ch)

	err := <-errChan
	return err
}

func toRDF(item kv, ch chan []byte) {
	p := new(types.Posting)
	pre := item.key
	buf := bytes.NewBuffer(make([]byte, 0, 100))

	pl := types.GetRootAsPostingList(item.value, 0)
	for pidx := 0; pidx < pl.PostingsLength(); pidx++ {
		x.AssertTruef(pl.Postings(p, pidx), "Unable to parse Posting at index: %v", pidx)
		buf.Write(pre)
		if p.Uid() == math.MaxUint64 && !bytes.Equal(p.ValueBytes(), nil) {
			// Value posting
			// Convert to appropriate type
			val := p.ValueBytes()
			typ := stype.ValueForType(stype.TypeID(p.ValType()))
			x.Check(typ.UnmarshalBinary(val))
			str, err := typ.MarshalText()
			x.Check(err)

			_, err = buf.WriteString(fmt.Sprintf("\"%s\"", str))
			x.Check(err)
			if p.ValType() != 0 {
				_, err = buf.WriteString(fmt.Sprintf("^^<xs:%s> ", typ.Type().Name))
				x.Check(err)
			}
			_, err = buf.WriteString(" .\n")
			x.Check(err)
			break
		}
		// Uid list
		destUID := p.Uid()

		_, err := buf.WriteString(fmt.Sprintf("<_uid_:%#x> .\n", destUID))
		x.Check(err)
		if buf.Len() >= 50000 {
			ch <- buf.Bytes()
			buf.Reset()
		}
	}
	ch <- buf.Bytes()
}

func writeToFile(str, bpath string, ch chan []byte, errChan chan error) {
	file := path.Join(bpath, fmt.Sprintf("dgraph-%s-%s.gz", str,
		time.Now().Format("2006-01-02-15-04")))
	err := os.MkdirAll(bpath, 0700)
	if err != nil {
		errChan <- err
	}
	f, err := os.Create(file)
	if err != nil {
		errChan <- err
	}
	defer f.Close()
	x.Check(err)
	w := bufio.NewWriterSize(f, 1000000)
	gw, err := gzip.NewWriterLevel(w, gzip.BestCompression)
	if err != nil {
		errChan <- err
		return
	}

	for item := range ch {
		gw.Write(item)
	}
	gw.Flush()
	gw.Close()
	w.Flush()
	errChan <- nil
}
