package bindata

import (
	"bytes"
	"encoding/binary"
	"math"
)

// BinaryWriter appends little-endian values. It writes the bytes
// binary.Write did, without its reflection and per-value allocation.
type BinaryWriter struct {
	buffer bytes.Buffer
}

func (self *BinaryWriter) Bytes() []byte {
	return self.buffer.Bytes()
}

func (self *BinaryWriter) Write(b []byte) (int, error) {
	return self.buffer.Write(b)
}

func (self *BinaryWriter) WriteInt16(x int16) error {
	return self.WriteUInt16(uint16(x))
}

func (self *BinaryWriter) WriteInt32(x int32) error {
	return self.WriteUInt32(uint32(x))
}

func (self *BinaryWriter) WriteInt64(x int64) error {
	return self.WriteUInt64(uint64(x))
}

func (self *BinaryWriter) WriteUInt16(x uint16) error {
	self.buffer.Write(binary.LittleEndian.AppendUint16(self.buffer.AvailableBuffer(), x))
	return nil
}

func (self *BinaryWriter) WriteUInt32(x uint32) error {
	self.buffer.Write(binary.LittleEndian.AppendUint32(self.buffer.AvailableBuffer(), x))
	return nil
}

func (self *BinaryWriter) WriteUInt64(x uint64) error {
	self.buffer.Write(binary.LittleEndian.AppendUint64(self.buffer.AvailableBuffer(), x))
	return nil
}

func (self *BinaryWriter) WriteFloat32(x float32) error {
	return self.WriteUInt32(math.Float32bits(x))
}

func (self *BinaryWriter) WriteFloat64(x float64) error {
	return self.WriteUInt64(math.Float64bits(x))
}

func (self *BinaryWriter) WriteBool(x bool) error {
	if x {
		return self.buffer.WriteByte(1)
	}
	return self.buffer.WriteByte(0)
}

func (self *BinaryWriter) WriteString(x string) error {
	if err := self.WriteUInt32(uint32(len(x))); err != nil {
		return err
	}
	_, err := self.buffer.WriteString(x)
	return err
}

func NewBinaryWriter() *BinaryWriter {
	return &BinaryWriter{}
}
