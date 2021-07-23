package audio

import (
	"bytes"
	"encoding/binary"
	"errors"
	"github.com/gordonklaus/portaudio"
	"goplay2/globals"
	"goplay2/ptp"
	"io"
	"log"
	"time"
)

type PlaybackStatus uint8

const (
	STOPPED PlaybackStatus = iota
	PLAYING
)

var underflow = errors.New("audio underflow")

type Stream interface {
	io.Closer
	Init(callBack func(out []int16, info portaudio.StreamCallbackTimeInfo)) error
	Start() error
	Stop() error
	CurrentTime() time.Duration
}

type audioSyncInfo struct {
	firstFrameTime      int64
	firstFrameTimestamp int64
	previousDrift       int64
}

type Player struct {
	ControlChannel chan globals.ControlMessage
	clock          *ptp.VirtualClock
	Status         PlaybackStatus
	stream         Stream
	ringBuffer     *Ring
	audioSync      audioSyncInfo
	syncLogger     *log.Logger
}

func NewPlayer(syncFile io.Writer, clock *ptp.VirtualClock, ring *Ring) *Player {
	return &Player{
		clock:          clock,
		ControlChannel: make(chan globals.ControlMessage, 100),
		stream:         NewStream(),
		Status:         STOPPED,
		ringBuffer:     ring,
		audioSync:      audioSyncInfo{},
		syncLogger:     log.New(syncFile, "SYNC: ", log.Ldate|log.Ltime|log.Lshortfile),
	}
}

func (p *Player) callBack(out []int16, info portaudio.StreamCallbackTimeInfo) {
	drift := p.clock.Now().Add(-p.stream.CurrentTime()).UnixNano()
	log.Printf("call back timing info : %v now : %v , clock :%v diff : %v\n",
		info, p.stream.CurrentTime(), p.clock.Now().UnixNano(), p.audioSync.previousDrift-drift)
	frame, err := p.ringBuffer.TryPop()
	if err == ErrIsEmpty {
		p.fillSilence(out)
	} else {
		err = binary.Read(bytes.NewReader(frame.(*PCMFrame).pcmData), binary.LittleEndian, out)
		if err != nil {
			globals.ErrLog.Printf("error reading data : %v\n", err)
		}
	}
	p.audioSync.previousDrift = drift
}

func (p *Player) Run() {
	var err error
	if err := p.stream.Init(p.callBack); err != nil {
		globals.ErrLog.Fatalln("Audio Stream init error:", err)
	}
	defer p.stream.Close()
	for {
		select {
		case msg := <-p.ControlChannel:
			switch msg.MType {
			case globals.PAUSE:
				if p.Status == PLAYING {
					if err := p.stream.Stop(); err != nil {
						globals.ErrLog.Printf("error pausing audio :%v\n", err)
						return
					}
				}
				p.Status = STOPPED
			case globals.START:
				if p.Status == STOPPED {
					err = p.stream.Start()
					if err != nil {
						globals.ErrLog.Printf("error while starting flow : %v\n", err)
						return
					}
				}
				p.Status = PLAYING
				p.audioSync.firstFrameTime = msg.Param1
				p.audioSync.firstFrameTimestamp = msg.Param2
			case globals.SKIP:
				p.skipUntil(msg.Param1, msg.Param2)
			}
		}
	}
}

func (p *Player) skipUntil(fromSeq int64, UntilSeq int64) {
	p.ringBuffer.Flush(func(value interface{}) bool {
		frame := value.(*PCMFrame)
		return frame.SequenceNumber < uint32(fromSeq) || frame.SequenceNumber > uint32(UntilSeq)
	})
}

func (p *Player) Push(frame interface{}) {
	p.ringBuffer.Push(frame)
}

func (p *Player) Reset() {
	p.ringBuffer.Reset()
}

func (p *Player) fillSilence(out []int16) {
	for i := range out {
		out[i] = 0
	}
}
