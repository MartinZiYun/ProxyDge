package goproxyproto

import (
	"bytes"
	"net"
	"testing"

	pp "proxydge/internal/proxyproto"
)

// benchTCP4Hdr mirrors what the stream reader produces for a consistent
// TCP4 header: 4-byte IPv4 addresses on both sides.
var benchTCP4Hdr = pp.Header{
	SrcIP:   net.IP{192, 0, 2, 1},
	DstIP:   net.IP{198, 51, 100, 1},
	SrcPort: 1234,
	DstPort: 80,
	Family:  pp.FamilyTCP4,
}

// benchmarkWriteTo measures WriteTo encode+write cost for one version/header
// shape into a reusable buffer (storage is Reset, not reallocated, so the
// numbers isolate serialization, not buffer growth).
func benchmarkWriteTo(ver byte, hdr pp.Header) func(*testing.B) {
	w := NewWriter(ver)
	return func(b *testing.B) {
		buf := bytes.NewBuffer(make([]byte, 0, 128))
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			buf.Reset()
			if err := w.WriteTo(buf, hdr); err != nil {
				b.Fatal(err)
			}
		}
	}
}

// BenchmarkWriterV2TCP4 / BenchmarkWriterV1TCP4 quantify the per-connection
// header-serialization gap between the binary v2 and text v1 wire formats.
func BenchmarkWriterV2TCP4(b *testing.B)         { benchmarkWriteTo(2, benchTCP4Hdr)(b) }
func BenchmarkWriterV1TCP4(b *testing.B)         { benchmarkWriteTo(1, benchTCP4Hdr)(b) }

// UNSPEC forms: the family-mismatch=unknown landing zone — v1 short
// "PROXY UNKNOWN\r\n" line vs the v2 LOCAL+AF_UNSPEC zero-address frame.
func BenchmarkWriterV2UnspecLocal(b *testing.B)  { benchmarkWriteTo(2, pp.Header{})(b) }
func BenchmarkWriterV1UnspecUnknown(b *testing.B){ benchmarkWriteTo(1, pp.Header{})(b) }
