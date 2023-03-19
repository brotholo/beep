package wav

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"os"

	"github.com/brotholo/beep"
	"github.com/pkg/errors"
)

// Encode writes all audio streamed from s to w in WAVE format.
//
// Format precision must be 1 or 2 bytes.
func Encode(w io.WriteSeeker, s beep.Streamer, format beep.Format) (err error) {
	defer func() {
		if err != nil {
			err = errors.Wrap(err, "wav")
		}
	}()

	if format.NumChannels <= 0 {
		return errors.New("wav: invalid number of channels (less than 1)")
	}
	if format.Precision != 1 && format.Precision != 2 && format.Precision != 3 {
		return errors.New("wav: unsupported precision, 1, 2 or 3 is supported")
	}

	h := header{
		RiffMark:      [4]byte{'R', 'I', 'F', 'F'},
		FileSize:      -1, // finalization
		WaveMark:      [4]byte{'W', 'A', 'V', 'E'},
		FmtMark:       [4]byte{'f', 'm', 't', ' '},
		FormatSize:    16,
		FormatType:    1,
		NumChans:      int16(format.NumChannels),
		SampleRate:    int32(format.SampleRate),
		ByteRate:      int32(int(format.SampleRate) * format.NumChannels * format.Precision),
		BytesPerFrame: int16(format.NumChannels * format.Precision),
		BitsPerSample: int16(format.Precision) * 8,
		DataMark:      [4]byte{'d', 'a', 't', 'a'},
		DataSize:      -1, // finalization
	}
	if err := binary.Write(w, binary.LittleEndian, &h); err != nil {
		return err
	}

	var (
		bw      = bufio.NewWriter(w)
		samples = make([][2]float64, 512)
		buffer  = make([]byte, len(samples)*format.Width())
		written int
	)
	for {
		n, ok := s.Stream(samples)
		if !ok {
			break
		}
		buf := buffer
		switch {
		case format.Precision == 1:
			for _, sample := range samples[:n] {
				buf = buf[format.EncodeUnsigned(buf, sample):]
			}
		case format.Precision == 2 || format.Precision == 3:
			for _, sample := range samples[:n] {
				buf = buf[format.EncodeSigned(buf, sample):]
			}
		default:
			panic(fmt.Errorf("wav: encode: invalid precision: %d", format.Precision))
		}
		nn, err := bw.Write(buffer[:n*format.Width()])
		if err != nil {
			return err
		}
		written += nn
	}
	if err := bw.Flush(); err != nil {
		return err
	}

	// finalize header
	h.FileSize = int32(44 + written) // 44 is the size of the header
	h.DataSize = int32(written)
	if _, err := w.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if err := binary.Write(w, binary.LittleEndian, &h); err != nil {
		return err
	}
	if _, err := w.Seek(0, io.SeekEnd); err != nil {
		return err
	}

	return nil
}

type EncodePerpetum struct {
	s       beep.Streamer
	format  beep.Format
	ask_ch  *chan bool
	resp_ch *chan []byte
	stop_ch *chan bool
	headers *header
	buff    *bytes.Buffer
	file    *io.WriteSeeker
}

func StartEncodePerpertum(
	s beep.Streamer,
	format beep.Format,
	ask_ch *chan bool,
	resp_ch *chan []byte,
	stop_ch *chan bool) bool {

	ep := EncodePerpetum{}
	ep.s = s
	ep.format = format
	ep.ask_ch = ask_ch
	ep.resp_ch = resp_ch
	ep.stop_ch = stop_ch
	if !ep.EncodeSetup() {
		return false
	}
	ep.Start()
	return false
}
func (ep *EncodePerpetum) NewFile(filename string) *os.File {
	wirgin, _ := os.Create(filename)
	if err := binary.Write(wirgin, binary.LittleEndian, ep.headers); err != nil {
		return nil
	}
	return wirgin
}

func (ep *EncodePerpetum) NewBuff() *bytes.Buffer {
	wirgin := bytes.NewBufferString("")
	if err := binary.Write(wirgin, binary.LittleEndian, ep.headers); err != nil {
		return nil
	}
	return wirgin
}
func (ep *EncodePerpetum) EncodeSetup() bool {
	if ep.format.NumChannels <= 0 {
		fmt.Println("wav: invalid number of channels (less than 1)")
		return false
	}
	if ep.format.Precision != 1 && ep.format.Precision != 2 && ep.format.Precision != 3 {
		fmt.Println("wav: unsupported precision, 1, 2 or 3 is supported")
		return false
	}
	ep.headers = ep.GetHeaders()
	return true
}

func (ep *EncodePerpetum) GetHeaders() *header {
	h := header{
		RiffMark:      [4]byte{'R', 'I', 'F', 'F'},
		FileSize:      -1, // finalization
		WaveMark:      [4]byte{'W', 'A', 'V', 'E'},
		FmtMark:       [4]byte{'f', 'm', 't', ' '},
		FormatSize:    16,
		FormatType:    1,
		NumChans:      int16(ep.format.NumChannels),
		SampleRate:    int32(ep.format.SampleRate),
		ByteRate:      int32(int(ep.format.SampleRate) * ep.format.NumChannels * ep.format.Precision),
		BytesPerFrame: int16(ep.format.NumChannels * ep.format.Precision),
		BitsPerSample: int16(ep.format.Precision) * 8,
		DataMark:      [4]byte{'d', 'a', 't', 'a'},
		DataSize:      -1, // finalization
	}
	return &h
}
func (ep *EncodePerpetum) WriteSamples(
	tbw *bufio.Writer,
	buffer []byte,
	written int,
	samples [][2]float64,
	nsamples int) (bool, int) {
	buf := buffer
	switch {
	case ep.format.Precision == 1:
		for _, sample := range samples[:nsamples] {
			buf = buf[ep.format.EncodeUnsigned(buf, sample):]
		}
	case ep.format.Precision == 2 || ep.format.Precision == 3:
		for _, sample := range samples[:nsamples] {
			buf = buf[ep.format.EncodeSigned(buf, sample):]
		}
	default:
		panic(fmt.Errorf("wav: encode: invalid precision: %d", ep.format.Precision))
	}
	nn, err := tbw.Write(buffer[:nsamples*ep.format.Width()])
	if err != nil {
		fmt.Println("BAD ERROR ReadSamples", err)
		return false, written
	}
	written += nn
	return true, written
}
func (ep *EncodePerpetum) ReadSamples() ([][2]float64, int) {
	samples := make([][2]float64, 512)
	n, ok := ep.s.Stream(samples)
	if !ok {
		return nil, n
	}
	return samples, n
}

func (ep *EncodePerpetum) FinalizeDataBuff(w *bytes.Buffer, tbw *bufio.Writer, written int) bool {
	if err := tbw.Flush(); err != nil {
		return false
	}
	headers := ep.GetHeaders()
	headers.FileSize = int32(44 + written) // 44 is the size of the header
	headers.DataSize = int32(written)
	if err := binary.Write(w, binary.LittleEndian, headers); err != nil {
		fmt.Println("ERROR FINALIZED DATA BUFF", err)
		return false
	}
	return true
}
func (ep *EncodePerpetum) FinalizeDataFile(w *os.File, tbw *bufio.Writer, written int) bool {
	if err := tbw.Flush(); err != nil {
		return false
	}
	// finalize header
	headers := ep.GetHeaders()
	headers.FileSize = int32(44 + written) // 44 is the size of the header
	headers.DataSize = int32(written)
	if _, err := w.Seek(0, io.SeekStart); err != nil {
		fmt.Println("ERROR FINALIZED DATA FILE seek start", err)
		return false
	}
	if err := binary.Write(w, binary.LittleEndian, headers); err != nil {
		fmt.Println("ERROR FINALIZED DATA FILE WRITE", err)
		return false
	}
	if _, err := w.Seek(0, io.SeekEnd); err != nil {
		fmt.Println("ERROR FINALIZED DATA FILE seek end", err)
		return false
	}
	w.Close()
	fmt.Println("FILE FINALIZZATO", w.Name())
	return true
}
func (ep *EncodePerpetum) Start() {
	//  var (
	short_buff := ep.NewBuff()
	long_buff := ep.NewBuff()
	//  filename := "debug_wav"
	//  fn_count := 0
	//  long_buff := ep.NewFile(filename + strconv.Itoa(fn_count) + ".wav")

	bw := bufio.NewWriter(short_buff)
	tbw := bufio.NewWriter(long_buff)
	fake_samples := make([][2]float64, 512)
	buffer := make([]byte, len(fake_samples)*ep.format.Width())
	written := 0
	twritten := 0

	for {
		select {
		case <-*ep.stop_ch:
			fmt.Println("STOP CHAN CALL")
			samples, nsamples := ep.ReadSamples()
			if samples == nil {
				fmt.Println("TMP BUFF IS NIL")
				return
			}
			ok, new_written := ep.WriteSamples(tbw, buffer, twritten, samples, nsamples)
			if !ok {
				fmt.Println("WRITE SAMPLES WRONG WRITE")
				return
			}
			twritten = new_written
			ep.FinalizeDataBuff(long_buff, tbw, twritten)
			//  ep.FinalizeDataFile(long_buff, tbw, twritten)
			//  fn_count += 1
			//  long_buff = ep.NewFile(filename + strconv.Itoa(fn_count) + ".wav")
			// NOT WORK YET
			*ep.resp_ch <- long_buff.Bytes()
			twritten = 0
			long_buff = ep.NewBuff()
			tbw = bufio.NewWriter(long_buff)
			//  *ep.resp_ch <- make([]byte, 0)

		case <-*ep.ask_ch:
			fmt.Println("ASK CHAN CALL")
			samples, nsamples := ep.ReadSamples()
			if samples == nil {
				fmt.Println("TMP BUFF IS NIL")
				return
			}
			ok, new_written := ep.WriteSamples(bw, buffer, written, samples, nsamples)
			if !ok {
				fmt.Println("WRITE SAMPLES WRONG WRITE")
				return
			}
			written = new_written
			ok, new_twritten := ep.WriteSamples(tbw, buffer, twritten, samples, nsamples)
			if !ok {
				fmt.Println("WRITE SAMPLES WRONG WRITE TBW")
				return
			}
			twritten = new_twritten
			ep.FinalizeDataBuff(short_buff, bw, written)
			*ep.resp_ch <- short_buff.Bytes()
			written = 0
			//  fmt.Println("resetto il soft", short_buff.Len())
			short_buff = ep.NewBuff()
			bw = bufio.NewWriter(short_buff)
			//  fmt.Println("POST resetto il soft", short_buff.Len())
		default:
			samples, nsamples := ep.ReadSamples()
			if samples == nil {
				fmt.Println("TMP BUFF IS NIL")
				return
			}
			ok, new_written := ep.WriteSamples(bw, buffer, written, samples, nsamples)
			if !ok {
				fmt.Println("WRITE SAMPLES WRONG WRITE")
				return
			}
			written = new_written
			ok, new_twritten := ep.WriteSamples(tbw, buffer, twritten, samples, nsamples)
			if !ok {
				fmt.Println("WRITE SAMPLES WRONG WRITE TBW")
				return
			}
			twritten = new_twritten
		}
	}
}
func EncodeBuff(w io.Writer, s beep.Streamer, format beep.Format) (err error) {
	defer func() {
		if err != nil {
			err = errors.Wrap(err, "wav")
		}
	}()

	if format.NumChannels <= 0 {
		return errors.New("wav: invalid number of channels (less than 1)")
	}
	if format.Precision != 1 && format.Precision != 2 && format.Precision != 3 {
		return errors.New("wav: unsupported precision, 1, 2 or 3 is supported")
	}

	h := header{
		RiffMark:      [4]byte{'R', 'I', 'F', 'F'},
		FileSize:      -1, // finalization
		WaveMark:      [4]byte{'W', 'A', 'V', 'E'},
		FmtMark:       [4]byte{'f', 'm', 't', ' '},
		FormatSize:    16,
		FormatType:    1,
		NumChans:      int16(format.NumChannels),
		SampleRate:    int32(format.SampleRate),
		ByteRate:      int32(int(format.SampleRate) * format.NumChannels * format.Precision),
		BytesPerFrame: int16(format.NumChannels * format.Precision),
		BitsPerSample: int16(format.Precision) * 8,
		DataMark:      [4]byte{'d', 'a', 't', 'a'},
		DataSize:      -1, // finalization
	}
	if err := binary.Write(w, binary.LittleEndian, &h); err != nil {
		return err
	}

	var (
		bw      = bufio.NewWriter(w)
		samples = make([][2]float64, 512)
		buffer  = make([]byte, len(samples)*format.Width())
		written int
	)
	for {
		n, ok := s.Stream(samples)
		if !ok {
			break
		}
		buf := buffer
		switch {
		case format.Precision == 1:
			for _, sample := range samples[:n] {
				buf = buf[format.EncodeUnsigned(buf, sample):]
			}
		case format.Precision == 2 || format.Precision == 3:
			for _, sample := range samples[:n] {
				buf = buf[format.EncodeSigned(buf, sample):]
			}
		default:
			panic(fmt.Errorf("wav: encode: invalid precision: %d", format.Precision))
		}
		nn, err := bw.Write(buffer[:n*format.Width()])
		if err != nil {
			return err
		}
		written += nn
	}
	if err := bw.Flush(); err != nil {
		return err
	}

	// finalize header
	h.FileSize = int32(44 + written) // 44 is the size of the header
	h.DataSize = int32(written)
	//  if _, err := w.Seek(0, io.SeekStart); err != nil {
	//  return err
	//  }
	if err := binary.Write(w, binary.LittleEndian, &h); err != nil {
		return err
	}
	//  if _, err := w.Seek(0, io.SeekEnd); err != nil {
	//  return err
	//  }

	return nil
}
