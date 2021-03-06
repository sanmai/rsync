package rsync

import (
	"errors"
	"fmt"
	"io"
	//"log"
)

var (
	whereBytes = map[uint8]uint32{
		RS_OP_COPY_N1_N1: 1,
		RS_OP_COPY_N1_N2: 1,
		RS_OP_COPY_N1_N4: 1,
		RS_OP_COPY_N1_N8: 1,
		RS_OP_COPY_N2_N1: 2,
		RS_OP_COPY_N2_N2: 2,
		RS_OP_COPY_N2_N4: 2,
		RS_OP_COPY_N2_N8: 2,
		RS_OP_COPY_N4_N1: 4,
		RS_OP_COPY_N4_N2: 4,
		RS_OP_COPY_N4_N4: 4,
		RS_OP_COPY_N4_N8: 4,
		RS_OP_COPY_N8_N1: 8,
		RS_OP_COPY_N8_N2: 8,
		RS_OP_COPY_N8_N4: 8,
		RS_OP_COPY_N8_N8: 8,
	}
	lengthBytes = map[uint8]uint32{
		RS_OP_COPY_N1_N1: 1,
		RS_OP_COPY_N1_N2: 2,
		RS_OP_COPY_N1_N4: 4,
		RS_OP_COPY_N1_N8: 8,
		RS_OP_COPY_N2_N1: 1,
		RS_OP_COPY_N2_N2: 2,
		RS_OP_COPY_N2_N4: 4,
		RS_OP_COPY_N2_N8: 8,
		RS_OP_COPY_N4_N1: 1,
		RS_OP_COPY_N4_N2: 2,
		RS_OP_COPY_N4_N4: 4,
		RS_OP_COPY_N4_N8: 8,
		RS_OP_COPY_N8_N1: 1,
		RS_OP_COPY_N8_N2: 2,
		RS_OP_COPY_N8_N4: 4,
		RS_OP_COPY_N8_N8: 8,

		RS_OP_LITERAL_N1: 1,
		RS_OP_LITERAL_N2: 2,
		RS_OP_LITERAL_N4: 4,
		RS_OP_LITERAL_N8: 8,
	}
	NotDeltaMagic = errors.New("Not delta file format: magic wrong")
)

type Patcher struct {
	deltaRd io.Reader
	target  io.ReadSeeker
	merged  io.Writer
	debug   bool
}

// 将差异直接写入target文件中，不单独创建merged文件
// deltaRd: delta文件
// target:  本地文件
func PatchSelf(deltaRd io.Reader, target io.ReadWriteSeeker, args ...bool) (err error) {
	return
}

// 将差异merged文件
// deltaRd: delta文件
// target:  本地文件
// merged:  合并后的文件
func Patch(deltaRd io.Reader, target io.ReadSeeker, merged io.Writer, args ...bool) (err error) {
	var (
		p      Patcher
		cmd    uint8
		wb     uint32
		lb     uint32
		magic  uint32
		where  uint64
		length uint64
	)

	// delta文件头：magic字段
	if magic, err = ntohl(deltaRd); err != nil {
		return fmt.Errorf("Read delta file magic failed: %s", err.Error())
	}
	if magic != DeltaMagic {
		return NotDeltaMagic
	}

	if len(args) > 0 {
		p.debug = args[0]
	}
	p.deltaRd = deltaRd
	p.merged = merged
	p.target = target
	// 分析matchStat
	for {
		if cmd, err = readByte(deltaRd); err == io.EOF {
			err = nil
			break
		}
		if cmd == 0 { // delta的结束命令
			break
		}
		//log.Printf("Patch cmd: 0x%x\n", cmd)
		if cmd >= RS_OP_COPY_N1_N1 && cmd <= RS_OP_COPY_N8_N8 {
			wb = whereBytes[cmd]
			lb = lengthBytes[cmd]
			if where, length, err = matchParams(deltaRd, wb, lb); err != nil {
				return
			}
			if err = p.patchMatch(where, length); err != nil {
				return
			}
		} else if cmd >= RS_OP_LITERAL_N1 && cmd <= RS_OP_LITERAL_N8 {
			lb = lengthBytes[cmd]
			if length, err = vRead(deltaRd, lb); err != nil {
				return
			}
			if err = p.patchMiss(length); err != nil {
				return
			}
		} else {
			panic(fmt.Sprintf("invalid delta command: %d", cmd))
		}
	}

	return
}

// 读取copy command的where和length参数
func matchParams(rd io.Reader, wb, lb uint32) (pos, length uint64, err error) {
	if pos, err = vRead(rd, wb); err != nil {
		err = fmt.Errorf("Read match param 'where'(length:%d) failed: %s",
			wb, err.Error())
		return
	}
	length, err = vRead(rd, lb)
	if err != nil {
		err = fmt.Errorf("Read match param 'length'(length:%d) failed: %s",
			lb, err.Error())
	}

	return
}

func pipe(r io.Reader, w io.Writer, l int64, debug bool) (err error) {
	var (
		n   int
		buf [4096]byte
	)

	for l > 0 {
		if l > 4096 {
			if n, err = io.ReadFull(r, buf[0:4096]); err != nil {
				err = fmt.Errorf("ReadFull failed: %s expect: %d read: %d",
					err.Error(), 4096, n)
				return
			}
			if n, err = w.Write(buf[0:4096]); err != nil {
				err = fmt.Errorf("Write failed: %s expect: %d write: %d",
					err.Error(), 4096, n)
				return
			}
		} else {
			n, err = io.ReadFull(r, buf[0:l])
			if int64(n) != l {
				Assert(err != nil, "err should Not be nil when readFull not complete.")
				return
			}
			Assert(err == nil,
				"err should nil when ReadFull complete.")

			if n, err = w.Write(buf[0:l]); err != nil {
				err = fmt.Errorf("Write failed: %s expect: %d write: %d",
					err.Error(), l, n)
				return
			}
			return
		}
		l -= 4096
	}

	return
}

// 处理match部分
func (p *Patcher) patchMatch(where, length uint64) (err error) {
	var offset int64

	if offset, err = p.target.Seek(int64(where), 0); err != nil {
		err = fmt.Errorf("seek target failed: where=%d error=%s", where, err.Error())
		return
	}
	if offset != int64(where) {
		return errors.New(fmt.Sprintf("should seek to %d but %d", where, offset))
	}

	err = pipe(p.target, p.merged, int64(length), p.debug)
	if err != nil {
		err = fmt.Errorf("patch match failed: where=%d length=%d error=%s", where, length, err.Error())
	}

	return
}

// 处理miss部分
func (p *Patcher) patchMiss(length uint64) (err error) {
	err = pipe(p.deltaRd, p.merged, int64(length), p.debug)
	if err != nil {
		err = fmt.Errorf("patch miss failed: length=%d error=%s", length, err.Error())
	}
	return
}
