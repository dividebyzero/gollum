// Copyright 2015-2017 trivago GmbH
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

package consumer

import (
	"github.com/fsnotify/fsnotify"
	"github.com/sirupsen/logrus"
	"github.com/trivago/gollum/core"
	"github.com/trivago/tgo"
	"github.com/trivago/tgo/tio"
	"github.com/trivago/tgo/tsync"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	fileBufferGrowSize = 1024
	fileOffsetStart    = "oldest"
	fileOffsetEnd      = "newest"
)

type fileState int32

const (
	fileStateOpen = fileState(iota)
	fileStateRead = fileState(iota)
	fileStateDone = fileState(iota)
)

const (
	observeModePoll  = "poll"
	observeModeWatch = "watch"
)

// File consumer plugin
//
// The file consumer allows to read from files while looking for a delimiter
// that marks the end of a message. If the file is part of e.g. a log rotation
// the file consumer can be set to a symbolic link of the latest file and
// (optionally) be told to reopen the file by sending a SIGHUP. A symlink to
// a file will automatically be reopened if the underlying file is changed.
//
// Configuration example
//
// myConsumer:
//   Type: consumer.File
//   File: /var/run/system.log
//   DefaultOffset: newest
//   OffsetFile: ""
//   Delimiter: "\n"
//   ObserveMode: poll
//   PollingDelay: 100
//
// File is a mandatory setting and contains the file to read. The file will be
// read from beginning to end and the reader will stay attached until the
// consumer is stopped. I.e. appends to the attached file will be recognized
// automatically.
//
// DefaultOffset defines where to start reading the file. Valid values are
// "oldest" and "newest". If OffsetFile is defined the DefaultOffset setting
// will be ignored unless the file does not exist.
// By default this is set to "newest".
//
// OffsetFile defines the path to a file that stores the current offset inside
// the given file. If the consumer is restarted that offset is used to continue
// reading. By default this is set to "" which disables the offset file.
//
// Delimiter defines the end of a message inside the file. By default this is
// set to "\n".
//
// ObserveMode defines the mode how to observe the target file. You can decide between `poll` and `watch`.
// By default this is set to `poll`.
// Note: The watch implementation uses [fsnotify/fsnotify](https://github.com/fsnotify/fsnotify) package.
// Please check for rotating files (like log rotation) if your system supports continue watching on moved files.
//
// PollingDelay defines the time duration how long the consumer will wait to check a file for new content
// after hitting the end of file (EOF) in milliseconds (ms).
// By default this time duration is set to "100" milliseconds.
// Note: This settings take only an effect if the consumer is running in `poll` mode!
type File struct {
	core.SimpleConsumer `gollumdoc:"embed_type"`

	delimiter   string `config:"Delimiter" default:"\n"`
	observeMode string `config:"ObserveMode" default:"poll"`

	seeker  seeker
	source  sourceFile
	watcher *watcher
}

func init() {
	core.TypeRegistry.Register(File{})
}

// Configure initializes this consumer with values from a plugin config.
func (cons *File) Configure(conf core.PluginConfigReader) {
	cons.SetRollCallback(cons.onRoll)

	var err error
	cons.source, err = newSourceFile(conf)
	if err != nil {
		cons.Logger.Fatal(err)
	}

	cons.seeker = newSeeker(conf)

	// restore default observer mode for invalid config settings
	if cons.observeMode != observeModePoll && cons.observeMode != observeModeWatch {
		cons.Logger.WithField("observeMode", cons.observeMode).Errorf("Unknown observe mode '%s'", cons.observeMode)
		cons.observeMode = observeModePoll
	}
}

// Enqueue creates a new message
func (cons *File) Enqueue(data []byte) {
	metaData := core.Metadata{}

	dir, file := filepath.Split(cons.source.realFileName)
	metaData.SetValue("file", []byte(file))
	metaData.SetValue("dir", []byte(dir))

	cons.EnqueueWithMetadata(data, metaData)
}

func (cons *File) storeOffset() {
	ioutil.WriteFile(cons.source.offsetFileName, []byte(strconv.FormatInt(cons.seeker.offset, 10)), 0644)
}

func (cons *File) enqueueAndPersist(data []byte) {
	cons.seeker.offset, _ = cons.source.file.Seek(0, 1)
	cons.Enqueue(data)
	cons.storeOffset()
}

func (cons *File) setState(state fileState) {
	cons.source.state = state
}

func (cons *File) initFile() {
	defer cons.setState(fileStateRead)

	if cons.source.file != nil {
		cons.source.file.Close()
		cons.source.file = nil
		cons.seeker.seek = cons.seeker.onRotate
		cons.seeker.offset = 0
		if cons.source.offsetFileName != "" {
			cons.storeOffset()
		}
	}

	if cons.source.offsetFileName != "" {
		fileContents, err := ioutil.ReadFile(cons.source.offsetFileName)
		if err == nil {
			cons.seeker.seek = 1
			cons.seeker.offset, err = strconv.ParseInt(string(fileContents), 10, 64)
			if err != nil {
				cons.Logger.Error("Error reading offset file: ", err)
			}
		}
	}
}

func (cons *File) close() {
	if cons.source.file != nil {
		cons.source.file.Close()
	}
	cons.setState(fileStateDone)
	cons.WorkerDone()
}

func (cons *File) observe() {
	defer cons.close()

	sendFunction := cons.Enqueue
	if cons.source.offsetFileName != "" {
		sendFunction = cons.enqueueAndPersist
	}

	buffer := tio.NewBufferedReader(fileBufferGrowSize, 0, 0, cons.delimiter)

	cons.Logger.WithField("file", cons.source.realFileName).Debugf("Use observe mode '%s'", cons.observeMode)
	if cons.observeMode == observeModeWatch {
		cons.watcher = newWatcher(cons.Logger, &cons.source, func() { cons.read(buffer, sendFunction, func() {}, func() {}) })
		cons.watcher.Watch(buffer, sendFunction)
	} else {
		cons.poll(buffer, sendFunction)
	}
}

func (cons *File) poll(buffer *tio.BufferedReader, sendFunction func(data []byte)) {
	spin := tsync.NewCustomSpinner(cons.source.pollingDelay)

	for cons.source.state != fileStateDone {
		cons.read(buffer, sendFunction, spin.Yield, spin.Reset)
	}
}

func (cons *File) read(buffer *tio.BufferedReader, sendFunction func(data []byte), onEOF func(), onAfterRead func()) {
	// Initialize the seek state if requested
	// Try to read the remains of the file first
	if cons.source.state == fileStateOpen {
		if cons.source.file != nil {
			buffer.ReadAll(cons.source.file, sendFunction)
		}
		cons.initFile()
		buffer.Reset(uint64(cons.seeker.offset))
	}

	// Try to open the file to read from
	if cons.source.state == fileStateRead && cons.source.file == nil {
		file, err := os.OpenFile(cons.source.realFileName, os.O_RDONLY, 0666)

		switch {
		case err != nil:
			if cons.source.printFileOpenError {
				cons.Logger.Warning("Open failed: ", err)
				cons.source.printFileOpenError = false
			}
			time.Sleep(3 * time.Second)
			return // ### continue, retry ###

		default:
			cons.source.file = file
			cons.seeker.offset, _ = cons.source.file.Seek(cons.seeker.offset, cons.seeker.seek)
			cons.source.printFileOpenError = true
		}
	}

	// Try to read from the file
	if cons.source.state == fileStateRead && cons.source.file != nil {
		err := buffer.ReadAll(cons.source.file, sendFunction)

		switch {
		case err == nil: // ok
			onAfterRead()

		case err == io.EOF:
			if cons.source.file.Name() != cons.source.realFileName {
				cons.Logger.Info("Rotation detected")
				cons.onRoll()
			} else {
				newStat, newStatErr := os.Stat(cons.source.realFileName)
				oldStat, oldStatErr := cons.source.file.Stat()

				if newStatErr == nil && oldStatErr == nil && !os.SameFile(newStat, oldStat) {
					cons.Logger.Info("Rotation detected")
					cons.onRoll()
				}
			}
			onEOF()

		case cons.source.state == fileStateRead:
			cons.Logger.Error("Reading failed: ", err)
			cons.source.file.Close()
			cons.source.file = nil
		}
	}
}

func (cons *File) onRoll() {
	cons.setState(fileStateOpen)
}

// Consume listens to stdin.
func (cons *File) Consume(workers *sync.WaitGroup) {
	cons.setState(fileStateOpen)
	defer cons.setState(fileStateDone)

	go tgo.WithRecoverShutdown(func() {
		cons.AddMainWorker(workers)
		cons.observe()
	})

	cons.ControlLoop()
}

// -- sourceFile --

type sourceFile struct {
	fileName       string        `config:"File" default:"/var/run/system.log"`
	offsetFileName string        `config:"OffsetFile"`
	pollingDelay   time.Duration `config:"PollingDelay" default:"100" metric:"ms"`

	file               *os.File
	realFileName       string
	state              fileState
	printFileOpenError bool
}

func (source *sourceFile) Configure(conf core.PluginConfigReader) {
	source.realFileName = source.getRealFileName()
	source.printFileOpenError = true
}

func (source *sourceFile) getRealFileName() string {
	baseFileName, err := filepath.EvalSymlinks(source.fileName)
	if err != nil {
		baseFileName = source.fileName
	}

	baseFileName, err = filepath.Abs(baseFileName)
	if err != nil {
		baseFileName = source.fileName
	}

	return baseFileName
}

func newSourceFile(conf core.PluginConfigReader) (sourceFile, error) {
	source := sourceFile{}
	err := conf.Configure(&source)
	if err != nil {
		return source, err
	}

	return source, nil
}

// -- seeker --

type seeker struct {
	seek     int
	onRotate int
	offset   int64
}

func newSeeker(conf core.PluginConfigReader) seeker {
	switch strings.ToLower(conf.GetString("DefaultOffset", fileOffsetEnd)) {
	default:
		fallthrough
	case fileOffsetEnd:
		return seeker{
			seek:     2,
			onRotate: 1,
			offset:   0,
		}

	case fileOffsetStart:
		return seeker{
			seek:     1,
			onRotate: 1,
			offset:   0,
		}
	}
}

// -- watcher --

type watcher struct {
	logger logrus.FieldLogger
	source *sourceFile
	read   func()

	done  chan int
	retry chan int
}

func newWatcher(logger logrus.FieldLogger, source *sourceFile, readFunction func()) *watcher {
	watcher := watcher{}

	watcher.logger = logger
	watcher.source = source
	watcher.read = readFunction

	return &watcher
}

func (w *watcher) close(watcher *fsnotify.Watcher) {
	err := watcher.Close()
	if err != nil {
		w.logger.Fatal(err)
	}
}

func (w *watcher) Watch(buffer *tio.BufferedReader, sendFunction func(data []byte)) {
	// init
	w.done = make(chan int)
	w.retry = make(chan int)

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		w.logger.Fatal(err)
	}
	defer w.close(watcher)

	// watcher init loop
	go func() {
		for {
			if _, err := os.Stat(w.source.realFileName); os.IsNotExist(err) {
				w.logger.WithField("file", w.source.realFileName).
					Warning("watched file not exists. retry in 3sec ..")
				time.Sleep(3 * time.Second)
				continue // retry
			}

			// read current file state before watching
			w.read()

			err = watcher.Add(w.source.realFileName)
			if err != nil {
				w.logger.Error("error during adding watcher: ", err)
				time.Sleep(3 * time.Second)
				continue // retry
			}

			// start watch loop to handle file events
			go w.watchLoop(watcher)

			// handle channel operations 'retry' and 'done'
			select {
			case <-w.retry:
				watcher.Remove(w.source.realFileName)
				//todo: reset reader?

				continue
			case <-w.done:
				return
			}
		}
	}()

	// busy loop for shutdown
	for w.source.state != fileStateDone {
		time.Sleep(time.Second)
	}

	w.logger.Debug("shutdown file watcher ..")
	close(w.done)
}

func (w *watcher) watchLoop(watcher *fsnotify.Watcher) {
	for {
		select {
		case event := <-watcher.Events:
			if event.Op&fsnotify.Write == fsnotify.Write {
				w.logger.Debug("modified file: ", event.Name)
				w.read()
				break
			}

			if event.Op&fsnotify.Rename == fsnotify.Rename || event.Op&fsnotify.Remove == fsnotify.Remove {
				w.logger.WithField("event", event).Debug("file renamed/removed: ", event.Name)

				w.retry <- 1
				return
			}
		case err := <-watcher.Errors:
			w.logger.Error("Error during watch loop: ", err)
		case <-w.done:
			return
		}
	}
}
