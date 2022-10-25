// SPDX-License-Identifier: AGPL-3.0-only

package client

import (
	"fmt"
	"io"
	"sync"

	"github.com/grafana/mimir/pkg/mimirpb"
)

const (
	maxInPoolChunksSliceSize = 16_384
)

var (
	chunkSlicesPool = sync.Pool{
		New: func() interface{} { return &[]Chunk{} },
	}
)

// IngesterQueryStreamClientWrappedReceiver extends the Ingester_QueryStreamClient interface
// adding a wrapped response receiver method.
type IngesterQueryStreamClientWrappedReceiver interface {
	Ingester_QueryStreamClient
	RecvWrapped(*WrappedQueryStreamResponse) error
}

func (x *ingesterQueryStreamClient) RecvWrapped(m *WrappedQueryStreamResponse) error {
	return x.ClientStream.RecvMsg(m)
}

type WrappedQueryStreamResponse struct {
	*QueryStreamResponse
}

func (m *WrappedQueryStreamResponse) Reset() {
	*m = WrappedQueryStreamResponse{&QueryStreamResponse{}}
}

// Unmarshal a WrappedQueryStreamResponse, implements proto.Unmarshaller.
// This is a copy of the autogenerated code to unmarshal a QueryStreamResponse,
// using wrapped timeseries chunks.
func (m *WrappedQueryStreamResponse) Unmarshal(dAtA []byte) error {
	l := len(dAtA)
	iNdEx := 0
	for iNdEx < l {
		preIndex := iNdEx
		var wire uint64
		for shift := uint(0); ; shift += 7 {
			if shift >= 64 {
				return ErrIntOverflowIngester
			}
			if iNdEx >= l {
				return io.ErrUnexpectedEOF
			}
			b := dAtA[iNdEx]
			iNdEx++
			wire |= uint64(b&0x7F) << shift
			if b < 0x80 {
				break
			}
		}
		fieldNum := int32(wire >> 3)
		wireType := int(wire & 0x7)
		if wireType == 4 {
			return fmt.Errorf("proto: QueryStreamResponse: wiretype end group for non-group")
		}
		if fieldNum <= 0 {
			return fmt.Errorf("proto: QueryStreamResponse: illegal tag %d (wire type %d)", fieldNum, wire)
		}
		switch fieldNum {
		case 1:
			if wireType != 2 {
				return fmt.Errorf("proto: wrong wireType = %d for field Chunkseries", wireType)
			}
			var msglen int
			for shift := uint(0); ; shift += 7 {
				if shift >= 64 {
					return ErrIntOverflowIngester
				}
				if iNdEx >= l {
					return io.ErrUnexpectedEOF
				}
				b := dAtA[iNdEx]
				iNdEx++
				msglen |= int(b&0x7F) << shift
				if b < 0x80 {
					break
				}
			}
			if msglen < 0 {
				return ErrInvalidLengthIngester
			}
			postIndex := iNdEx + msglen
			if postIndex < 0 {
				return ErrInvalidLengthIngester
			}
			if postIndex > l {
				return io.ErrUnexpectedEOF
			}
			// << NON AUTO-GENERATED CODE >>
			//
			//  m.Chunkseries = append(m.Chunkseries, TimeSeriesChunk{})
			//  if err := m.Chunkseries[len(m.Chunkseries)-1].Unmarshal(dAtA[iNdEx:postIndex]); err != nil {
			//      return err
			//  }
			//
			ts := WrappedTimeSeriesChunk{TimeSeriesChunk: &TimeSeriesChunk{}}
			if err := ts.Unmarshal(dAtA[iNdEx:postIndex]); err != nil {
				return err
			}
			m.Chunkseries = append(m.Chunkseries, *ts.TimeSeriesChunk)
			//<< NON AUTO-GENERATED CODE >>
			iNdEx = postIndex
		case 2:
			if wireType != 2 {
				return fmt.Errorf("proto: wrong wireType = %d for field Timeseries", wireType)
			}
			var msglen int
			for shift := uint(0); ; shift += 7 {
				if shift >= 64 {
					return ErrIntOverflowIngester
				}
				if iNdEx >= l {
					return io.ErrUnexpectedEOF
				}
				b := dAtA[iNdEx]
				iNdEx++
				msglen |= int(b&0x7F) << shift
				if b < 0x80 {
					break
				}
			}
			if msglen < 0 {
				return ErrInvalidLengthIngester
			}
			postIndex := iNdEx + msglen
			if postIndex < 0 {
				return ErrInvalidLengthIngester
			}
			if postIndex > l {
				return io.ErrUnexpectedEOF
			}
			m.Timeseries = append(m.Timeseries, mimirpb.TimeSeries{})
			if err := m.Timeseries[len(m.Timeseries)-1].Unmarshal(dAtA[iNdEx:postIndex]); err != nil {
				return err
			}
			iNdEx = postIndex
		default:
			iNdEx = preIndex
			skippy, err := skipIngester(dAtA[iNdEx:])
			if err != nil {
				return err
			}
			if skippy < 0 {
				return ErrInvalidLengthIngester
			}
			if (iNdEx + skippy) < 0 {
				return ErrInvalidLengthIngester
			}
			if (iNdEx + skippy) > l {
				return io.ErrUnexpectedEOF
			}
			iNdEx += skippy
		}
	}

	if iNdEx > l {
		return io.ErrUnexpectedEOF
	}
	return nil
}

type WrappedTimeSeriesChunk struct {
	*TimeSeriesChunk
}

func (m *WrappedTimeSeriesChunk) Reset() {
	*m = WrappedTimeSeriesChunk{&TimeSeriesChunk{}}
}

// Unmarshal a WrappedTimeSeriesChunk, implements proto.Unmarshaller.
// This is a copy of the autogenerated code to unmarshal a TimeSeriesChunk,
// fetching chunk objects from a buffer pool.
func (m *WrappedTimeSeriesChunk) Unmarshal(dAtA []byte) error {
	// << NON AUTO-GENERATED CODE >>
	m.Chunks = *(chunkSlicesPool.Get().(*[]Chunk))

	reusedChunks := 0
	poolChunksLength := len(m.Chunks)

	defer func() {
		// Readjust chunks slice length in case not all available slots have been reused.
		if reusedChunks < poolChunksLength {
			m.Chunks = m.Chunks[:reusedChunks]
		}
	}()
	// << NON AUTO-GENERATED CODE >>

	l := len(dAtA)
	iNdEx := 0
	for iNdEx < l {
		preIndex := iNdEx
		var wire uint64
		for shift := uint(0); ; shift += 7 {
			if shift >= 64 {
				return ErrIntOverflowIngester
			}
			if iNdEx >= l {
				return io.ErrUnexpectedEOF
			}
			b := dAtA[iNdEx]
			iNdEx++
			wire |= uint64(b&0x7F) << shift
			if b < 0x80 {
				break
			}
		}
		fieldNum := int32(wire >> 3)
		wireType := int(wire & 0x7)
		if wireType == 4 {
			return fmt.Errorf("proto: TimeSeriesChunk: wiretype end group for non-group")
		}
		if fieldNum <= 0 {
			return fmt.Errorf("proto: TimeSeriesChunk: illegal tag %d (wire type %d)", fieldNum, wire)
		}
		switch fieldNum {
		case 1:
			if wireType != 2 {
				return fmt.Errorf("proto: wrong wireType = %d for field FromIngesterId", wireType)
			}
			var stringLen uint64
			for shift := uint(0); ; shift += 7 {
				if shift >= 64 {
					return ErrIntOverflowIngester
				}
				if iNdEx >= l {
					return io.ErrUnexpectedEOF
				}
				b := dAtA[iNdEx]
				iNdEx++
				stringLen |= uint64(b&0x7F) << shift
				if b < 0x80 {
					break
				}
			}
			intStringLen := int(stringLen)
			if intStringLen < 0 {
				return ErrInvalidLengthIngester
			}
			postIndex := iNdEx + intStringLen
			if postIndex < 0 {
				return ErrInvalidLengthIngester
			}
			if postIndex > l {
				return io.ErrUnexpectedEOF
			}
			m.FromIngesterId = string(dAtA[iNdEx:postIndex])
			iNdEx = postIndex
		case 2:
			if wireType != 2 {
				return fmt.Errorf("proto: wrong wireType = %d for field UserId", wireType)
			}
			var stringLen uint64
			for shift := uint(0); ; shift += 7 {
				if shift >= 64 {
					return ErrIntOverflowIngester
				}
				if iNdEx >= l {
					return io.ErrUnexpectedEOF
				}
				b := dAtA[iNdEx]
				iNdEx++
				stringLen |= uint64(b&0x7F) << shift
				if b < 0x80 {
					break
				}
			}
			intStringLen := int(stringLen)
			if intStringLen < 0 {
				return ErrInvalidLengthIngester
			}
			postIndex := iNdEx + intStringLen
			if postIndex < 0 {
				return ErrInvalidLengthIngester
			}
			if postIndex > l {
				return io.ErrUnexpectedEOF
			}
			m.UserId = string(dAtA[iNdEx:postIndex])
			iNdEx = postIndex
		case 3:
			if wireType != 2 {
				return fmt.Errorf("proto: wrong wireType = %d for field Labels", wireType)
			}
			var msglen int
			for shift := uint(0); ; shift += 7 {
				if shift >= 64 {
					return ErrIntOverflowIngester
				}
				if iNdEx >= l {
					return io.ErrUnexpectedEOF
				}
				b := dAtA[iNdEx]
				iNdEx++
				msglen |= int(b&0x7F) << shift
				if b < 0x80 {
					break
				}
			}
			if msglen < 0 {
				return ErrInvalidLengthIngester
			}
			postIndex := iNdEx + msglen
			if postIndex < 0 {
				return ErrInvalidLengthIngester
			}
			if postIndex > l {
				return io.ErrUnexpectedEOF
			}
			m.Labels = append(m.Labels, mimirpb.LabelAdapter{})
			if err := m.Labels[len(m.Labels)-1].Unmarshal(dAtA[iNdEx:postIndex]); err != nil {
				return err
			}
			iNdEx = postIndex
		case 4:
			if wireType != 2 {
				return fmt.Errorf("proto: wrong wireType = %d for field Chunks", wireType)
			}
			var msglen int
			for shift := uint(0); ; shift += 7 {
				if shift >= 64 {
					return ErrIntOverflowIngester
				}
				if iNdEx >= l {
					return io.ErrUnexpectedEOF
				}
				b := dAtA[iNdEx]
				iNdEx++
				msglen |= int(b&0x7F) << shift
				if b < 0x80 {
					break
				}
			}
			if msglen < 0 {
				return ErrInvalidLengthIngester
			}
			postIndex := iNdEx + msglen
			if postIndex < 0 {
				return ErrInvalidLengthIngester
			}
			if postIndex > l {
				return io.ErrUnexpectedEOF
			}
			// << NON AUTO-GENERATED CODE >>
			//
			//  m.Chunks = append(m.Chunks, Chunk{})
			//  if err := m.Chunks[len(m.Chunks)-1].Unmarshal(dAtA[iNdEx:postIndex]); err != nil {
			//      return err
			//  }
			//
			if reusedChunks < poolChunksLength {
				if err := m.Chunks[reusedChunks].Unmarshal(dAtA[iNdEx:postIndex]); err != nil {
					return err
				}
				reusedChunks++
			} else {
				m.Chunks = append(m.Chunks, Chunk{})
				if err := m.Chunks[len(m.Chunks)-1].Unmarshal(dAtA[iNdEx:postIndex]); err != nil {
					return err
				}
			}
			// << NON AUTO-GENERATED CODE >>
			iNdEx = postIndex
		default:
			iNdEx = preIndex
			skippy, err := skipIngester(dAtA[iNdEx:])
			if err != nil {
				return err
			}
			if skippy < 0 {
				return ErrInvalidLengthIngester
			}
			if (iNdEx + skippy) < 0 {
				return ErrInvalidLengthIngester
			}
			if (iNdEx + skippy) > l {
				return io.ErrUnexpectedEOF
			}
			iNdEx += skippy
		}
	}

	if iNdEx > l {
		return io.ErrUnexpectedEOF
	}
	return nil
}

// ReuseQueryStreamResponse puts all chunks slices contained in a query stream response back into a sync.Pool for reuse.
func ReuseQueryStreamResponse(m *QueryStreamResponse) {
	for i := 0; i < len(m.Chunkseries); i++ {
		if len(m.Chunkseries[i].Chunks) > maxInPoolChunksSliceSize {
			continue
		}
		chunkSlicesPool.Put(&m.Chunkseries[i].Chunks)
	}
}