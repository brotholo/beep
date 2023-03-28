package wav

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"

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
	s                          beep.Streamer
	format                     beep.Format
	ask_ch                     *chan bool
	rbuff_ch                   *chan []byte
	rtext_ch                   *chan string
	rsamples_ch                *chan [][][2]float64
	stop_ch                    *chan bool
	wakeup_time                int
	min_vol_start_rec          float64
	max_vol_stop_rec           float64
	autobalance_start_stop_rec bool
	headers                    *header
	buff                       *bytes.Buffer
	file                       *io.WriteSeeker
}

func StartEncodePerpertum(
	s beep.Streamer,
	format beep.Format,
	ask_ch *chan bool,
	rbuff_ch *chan []byte,
	rtext_ch *chan string,
	rsamples_ch *chan [][][2]float64,
	stop_ch *chan bool,
	wakeup_time int,
	min_vol_start_rec float64,
	max_vol_stop_rec float64,
	autobalance_start_stop_rec bool,
	debug_file bool,
	debug_samples bool) bool {

	ep := EncodePerpetum{}
	ep.s = s
	ep.format = format
	ep.ask_ch = ask_ch
	ep.rbuff_ch = rbuff_ch
	ep.rtext_ch = rtext_ch
	ep.rsamples_ch = rsamples_ch
	ep.stop_ch = stop_ch
	ep.wakeup_time = wakeup_time
	ep.min_vol_start_rec = min_vol_start_rec
	ep.max_vol_stop_rec = max_vol_stop_rec
	ep.autobalance_start_stop_rec = autobalance_start_stop_rec
	if !ep.EncodeSetup() {
		return false
	}
	//  ep.StartNoSilence()
	ep.StartWithDetect(debug_file, debug_samples)
	return false
}
func (ep *EncodePerpetum) NewFile(filename string) *os.File {
	wirgin, _ := os.Create(filename)
	if err := binary.Write(wirgin, binary.LittleEndian, ep.headers); err != nil {
		return nil
	}
	return wirgin
}

func (ep *EncodePerpetum) AddSilence(tbw *bufio.Writer, seconds int) int {
	silence := [][2]float64{}
	written := 0
	for i := 0; i < 512; i += 1 {
		silence = append(silence, [2]float64{0, 0})
	}
	for t := 0; t < (seconds * 16000 / 512); t += 1 {
		_, written = ep.WriteSamples(tbw, 0, silence, len(silence))
	}
	return written

}
func (ep *EncodePerpetum) NewBuff() (*bytes.Buffer, *bufio.Writer, int) {
	wirgin := bytes.NewBufferString("")
	if err := binary.Write(wirgin, binary.LittleEndian, ep.headers); err != nil {
		return nil, nil, 0
	}
	bw := bufio.NewWriter(wirgin)
	//  new_written := ep.AddSilence(bw, 1)
	new_written := 0
	return wirgin, bw, new_written
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
	written int,
	samples [][2]float64,
	nsamples int) (bool, int) {
	fake_samples := make([][2]float64, 512)
	buffer := make([]byte, len(fake_samples)*ep.format.Width())
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
	defer w.Close()
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
	fmt.Println("FILE FINALIZZATO", w.Name())
	return true
}

type WakeUp struct {
	tts              int
	fake_break       int
	fake_break_limit int
	nsamples_rec     int
	record_mode      bool
	back_to_silence  int
	threshold        float64
	th_on            float64
	th_off           float64
	autobalance      bool
	bottom_memory    [31][][2]float64
	complete_samples [][][2]float64
}

func InitWakeUp(tts int, th_on float64, th_off float64, autobalance bool) *WakeUp {
	wu := WakeUp{}
	wu.tts = tts
	wu.fake_break = 0
	wu.fake_break_limit = 2
	wu.back_to_silence = 0
	wu.nsamples_rec = 0
	wu.record_mode = false
	wu.th_on = th_on
	wu.th_off = th_off
	wu.autobalance = autobalance
	wu.threshold = wu.th_off
	wu.bottom_memory = [31][][2]float64{}
	//  wu.autobalance_buff = [][][2]float64{}
	wu.complete_samples = [][][2]float64{}
	return &wu
}

func (wu *WakeUp) RefreshMem() [31][][2]float64 {
	wb := wu.bottom_memory
	wu.bottom_memory = [31][][2]float64{}
	return wb

}
func (wu *WakeUp) BackupSamples(samples [][2]float64, nsamples int) {
	for i := 0; i < 30; i += 1 {
		wu.bottom_memory[i] = wu.bottom_memory[i+1]
	}
	wu.bottom_memory[30] = samples
}
func (wu *WakeUp) CheckAutobalance(samples [][2]float64, nsamples int) string {
	current_svar := IsSilent(samples, wu.threshold, false, false)
	switch {
	case wu.record_mode:
		//  fmt.Println(wu.back_to_silence, wu.tts)
		if current_svar {
			//  if wu.back_to_silence > wu.tts {
			//  wu.record_mode = false
			//  wu.back_to_silence = 0
			//  fmt.Println("Back To Silent")
			//  if wu.fake_break < wu.fake_break_limit {
			//  return "drop"
			//  }
			//  wu.threshold = wu.th_off
			//  return "complete"
			//  } else {
			//  wu.back_to_silence += 1
			//  return "continue"
			//  }
		} else {
			//  fmt.Println("RESET BACK TO SILENCE")
			//  wu.back_to_silence = 0
			//  wu.fake_break += 1
			//  return "continue"
		}
	case !wu.record_mode:
		//  if !current_svar {
		//  fmt.Println("SILENCE BREAK")
		//  wu.record_mode = true
		//  wu.threshold = wu.th_on
		//  return "init"
		//  } else {
		//  wu.BackupSamples(samples, nsamples)
		//  return ""
		//  }
	}

	return "complete"
}
func (wu *WakeUp) Check(samples [][2]float64, nsamples int) string {
	current_svar := IsSilent(samples, wu.threshold, false, false)
	switch {
	case wu.record_mode:
		//  fmt.Println(wu.back_to_silence, wu.tts)
		//  wu.complete_samples = append(wu.complete_samples, samples)
		if current_svar {
			if wu.back_to_silence > wu.tts {
				wu.record_mode = false
				wu.back_to_silence = 0
				fmt.Println("Back To Silent")
				if wu.fake_break < wu.fake_break_limit {
					return "drop"
				}
				wu.threshold = wu.th_off
				return "complete"
			} else {
				wu.back_to_silence += 1
				return "continue"
			}
		} else {
			fmt.Println("RESET BACK TO SILENCE")
			wu.back_to_silence = 0
			wu.fake_break += 1
			return "continue"
		}
	case !wu.record_mode:
		if !current_svar {
			fmt.Println("SILENCE BREAK")
			wu.record_mode = true
			wu.threshold = wu.th_on
			return "init"
		} else {
			wu.BackupSamples(samples, nsamples)
			return ""
		}
	}

	return "complete"
}

func (ep *EncodePerpetum) StartWithDetect(debug_audio_file bool, debug_samples bool) {
	long_buff, tbw, twritten := ep.NewBuff()
	filename := "debug_wav"
	fn_count := 0
	wakeUp := InitWakeUp(ep.wakeup_time,
		ep.min_vol_start_rec,
		ep.max_vol_stop_rec,
		ep.autobalance_start_stop_rec)
	for {
		samples, nsamples := ep.ReadSamples()
		if samples == nil {
			fmt.Println("TMP BUFF IS NIL")
			return
		}
		var res string
		//  if ep.autobalance_start_stop_rec {
		//  res = wakeUp.CheckAutobalance(samples, nsamples)
		//  } else {
		res = wakeUp.Check(samples, nsamples)
		//  }
		switch res {
		case "complete":
			fmt.Println("COMPLETE")
			ok, new_twritten := ep.WriteSamples(tbw, twritten, samples, nsamples)
			if ep.autobalance_start_stop_rec {
				wakeUp.complete_samples = append(wakeUp.complete_samples, samples)
			}
			if !ok {
				fmt.Println("WRITE SAMPLES WRONG WRITE TBW")
				return
			}
			twritten = new_twritten
			ep.FinalizeDataBuff(long_buff, tbw, twritten)
			*ep.rbuff_ch <- long_buff.Bytes()
			if debug_samples {
				*ep.rsamples_ch <- wakeUp.complete_samples
			}
			if ep.autobalance_start_stop_rec {
				wakeUp.complete_samples = [][][2]float64{}
			}
			if debug_audio_file {
				fn_count += 1
				nfname := filename + strconv.Itoa(fn_count) + ".wav"
				tmp_f := ep.NewFile(nfname)
				tmp_f.Write(long_buff.Bytes())
				tmp_f.Close()
				*ep.rtext_ch <- nfname
			}

			long_buff, tbw, twritten = ep.NewBuff()
		case "init":
			fmt.Println("INIT", twritten)
			lsamples := wakeUp.RefreshMem()
			for _, ss := range lsamples {
				ok, new_twritten := ep.WriteSamples(tbw, twritten, ss, len(ss))
				if ep.autobalance_start_stop_rec {
					wakeUp.complete_samples = append(wakeUp.complete_samples, samples)
				}
				if !ok {
					fmt.Println("WRITE SAMPLES WRONG WRITE TBW")
					return
				}
				twritten = new_twritten
			}
			ok, new_twritten := ep.WriteSamples(tbw, twritten, samples, nsamples)
			if ep.autobalance_start_stop_rec {
				wakeUp.complete_samples = append(wakeUp.complete_samples, samples)
			}
			if !ok {
				fmt.Println("WRITE SAMPLES WRONG WRITE TBW")
				return
			}
			twritten = new_twritten
			fmt.Println("INIT DONE", twritten)
		case "drop":
			fmt.Println("DROP")
			long_buff, tbw, twritten = ep.NewBuff()
		case "continue":
			ok, new_twritten := ep.WriteSamples(tbw, twritten, samples, nsamples)
			if ep.autobalance_start_stop_rec {
				wakeUp.complete_samples = append(wakeUp.complete_samples, samples)
			}
			if !ok {
				fmt.Println("WRITE SAMPLES WRONG WRITE TBW")
				return
			}
			twritten = new_twritten
		}
	}
}

func GetMaxValSample(snd_data [][2]float64) float64 {
	max_sample := float64(0)
	for _, s := range snd_data {
		if math.Abs(s[0]) > max_sample {
			max_sample = s[0]
		}
	}
	//  fmt.Println(max_sample)
	return max_sample
}
func IsSilent(snd_data [][2]float64, threshold float64, logmin bool, logmax bool) bool {
	max_sample := GetMaxValSample(snd_data)
	res := max_sample < threshold

	if logmin && res {
		fmt.Println("MAX SAMPLE", max_sample)
	}
	if logmax && !res {
		fmt.Println("MAX SAMPLE", max_sample)
	}
	return res
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
