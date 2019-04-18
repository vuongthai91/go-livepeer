package monitor

import (
	"context"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/golang/glog"

	rprom "github.com/prometheus/client_golang/prometheus"
	"go.opencensus.io/exporter/prometheus"
	"go.opencensus.io/stats"
	"go.opencensus.io/stats/view"
	"go.opencensus.io/tag"
)

type (
	SegmentUploadError    string
	SegmentTranscodeError string
)

const (
	SegmentUploadErrorUnknown               SegmentUploadError    = "Unknown"
	SegmentUploadErrorGenCreds              SegmentUploadError    = "GenCreds"
	SegmentUploadErrorOS                    SegmentUploadError    = "ObjectStorage"
	SegmentUploadErrorSessionEnded          SegmentUploadError    = "SessionEnded"
	SegmentUploadErrorTimeout               SegmentUploadError    = "Timeout"
	SegmentTranscodeErrorUnknown            SegmentTranscodeError = "Unknown"
	SegmentTranscodeErrorUnknownResponse    SegmentTranscodeError = "UnknownResponse"
	SegmentTranscodeErrorTranscode          SegmentTranscodeError = "Transcode"
	SegmentTranscodeErrorOrchestratorBusy   SegmentTranscodeError = "OrchestratorBusy"
	SegmentTranscodeErrorOrchestratorCapped SegmentTranscodeError = "OrchestratorCapped"
	SegmentTranscodeErrorParseResponse      SegmentTranscodeError = "ParseResponse"
	SegmentTranscodeErrorReadBody           SegmentTranscodeError = "ReadBody"
	SegmentTranscodeErrorNoOrchestrators    SegmentTranscodeError = "NoOrchestrators"
	SegmentTranscodeErrorDownload           SegmentTranscodeError = "Download"
	SegmentTranscodeErrorSaveData           SegmentTranscodeError = "SaveData"
	SegmentTranscodeErrorSessionEnded       SegmentTranscodeError = "SessionEnded"
	SegmentTranscodeErrorPlaylist           SegmentTranscodeError = "Playlist"

	numberOfSegmentsToCalcAverage = 30
)

var timeToWaitForError = 8500 * time.Millisecond
var timeoutWatcherPause = 15 * time.Second

type (
	censusMetricsCounter struct {
		nodeType                      string
		nodeID                        string
		ctx                           context.Context
		kNodeType                     tag.Key
		kNodeID                       tag.Key
		kProfile                      tag.Key
		kProfiles                     tag.Key
		kErrorCode                    tag.Key
		mSegmentSourceAppeared        *stats.Int64Measure
		mSegmentEmerged               *stats.Int64Measure
		mSegmentEmergedWithProfiles   *stats.Int64Measure
		mSegmentUploaded              *stats.Int64Measure
		mSegmentUploadFailed          *stats.Int64Measure
		mSegmentTranscoded            *stats.Int64Measure
		mSegmentTranscodeFailed       *stats.Int64Measure
		mSegmentTranscodedAppeared    *stats.Int64Measure
		mSegmentTranscodedAllAppeared *stats.Int64Measure
		mStartBroadcastClientFailed   *stats.Int64Measure
		mStreamCreateFailed           *stats.Int64Measure
		mStreamCreated                *stats.Int64Measure
		mStreamStarted                *stats.Int64Measure
		mStreamEnded                  *stats.Int64Measure
		mMaxSessions                  *stats.Int64Measure
		mCurrentSessions              *stats.Int64Measure
		mDiscoveryError               *stats.Int64Measure
		mSuccessRate                  *stats.Float64Measure
		mTranscodeTime                *stats.Float64Measure
		mTranscodeLatency             *stats.Float64Measure
		mTranscodeOverallLatency      *stats.Float64Measure
		mUploadTime                   *stats.Float64Measure
		lock                          sync.Mutex
		emergeTimes                   map[uint64]map[uint64]time.Time // nonce:seqNo
		success                       map[uint64]*segmentsAverager
	}

	segmentCount struct {
		seqNo       uint64
		emergedTime time.Time
		emerged     int
		transcoded  int
		failed      bool
	}

	segmentsAverager struct {
		segments  []segmentCount
		start     int
		end       int
		removed   bool
		removedAt time.Time
	}
)

// Exporter Prometheus exporter that handles `/metrics` endpoint
var Exporter *prometheus.Exporter

var census censusMetricsCounter

func initCensus(nodeType, nodeID, version string, testing bool) {
	census = censusMetricsCounter{
		emergeTimes: make(map[uint64]map[uint64]time.Time),
		nodeID:      nodeID,
		nodeType:    nodeType,
		success:     make(map[uint64]*segmentsAverager),
	}
	var err error
	census.kNodeType, _ = tag.NewKey("node_type")
	census.kNodeID, _ = tag.NewKey("node_id")
	census.kProfile, _ = tag.NewKey("profile")
	census.kProfiles, _ = tag.NewKey("profiles")
	census.kErrorCode, _ = tag.NewKey("error_code")
	census.ctx, err = tag.New(context.Background(), tag.Insert(census.kNodeType, nodeType), tag.Insert(census.kNodeID, nodeID))
	if err != nil {
		glog.Fatal("Error creating context", err)
	}
	census.mSegmentSourceAppeared = stats.Int64("segment_source_appeared_total", "SegmentSourceAppeared", "tot")
	census.mSegmentEmerged = stats.Int64("segment_source_emerged_total", "SegmentEmerged", "tot")
	census.mSegmentEmergedWithProfiles = stats.Int64("segment_source_emerged_with_profiles_total", "SegmentEmerged, counted by number of transcode profiles", "tot")
	census.mSegmentUploaded = stats.Int64("segment_source_uploaded_total", "SegmentUploaded", "tot")
	census.mSegmentUploadFailed = stats.Int64("segment_source_upload_failed_total", "SegmentUploadedFailed", "tot")
	census.mSegmentTranscoded = stats.Int64("segment_transcoded_total", "SegmentTranscoded", "tot")
	census.mSegmentTranscodeFailed = stats.Int64("segment_transcode_failed_total", "SegmentTranscodeFailed", "tot")
	census.mSegmentTranscodedAppeared = stats.Int64("segment_transcoded_appeared_total", "SegmentTranscodedAppeared", "tot")
	census.mSegmentTranscodedAllAppeared = stats.Int64("segment_transcoded_all_appeared_total", "SegmentTranscodedAllAppeared", "tot")
	census.mStartBroadcastClientFailed = stats.Int64("broadcast_client_start_failed_total", "StartBroadcastClientFailed", "tot")
	census.mStreamCreateFailed = stats.Int64("stream_create_failed_total", "StreamCreateFailed", "tot")
	census.mStreamCreated = stats.Int64("stream_created_total", "StreamCreated", "tot")
	census.mStreamStarted = stats.Int64("stream_started_total", "StreamStarted", "tot")
	census.mStreamEnded = stats.Int64("stream_ended_total", "StreamEnded", "tot")
	census.mMaxSessions = stats.Int64("max_sessions_total", "MaxSessions", "tot")
	census.mCurrentSessions = stats.Int64("current_sessions_total", "Number of currently transcded streams", "tot")
	census.mDiscoveryError = stats.Int64("discovery_errors_total", "Number of discover errors", "tot")
	census.mSuccessRate = stats.Float64("success_rate", "Success rate", "per")
	census.mTranscodeTime = stats.Float64("transcode_time_seconds", "Transcoding time", "sec")
	census.mTranscodeLatency = stats.Float64("transcode_latency_seconds",
		"Transcoding latency, from source segment emered from segmenter till transcoded segment apeeared in manifest", "sec")
	census.mTranscodeOverallLatency = stats.Float64("transcode_overall_latency_seconds",
		"Transcoding latency, from source segment emered from segmenter till all transcoded segment apeeared in manifest", "sec")
	census.mUploadTime = stats.Float64("upload_time_seconds", "Upload (to Orchestrator) time", "sec")

	glog.Infof("Compiler: %s Arch %s OS %s Go version %s", runtime.Compiler, runtime.GOARCH, runtime.GOOS, runtime.Version())
	glog.Infof("Livepeer version: %s", version)
	glog.Infof("Node type %s node ID %s", nodeType, nodeID)
	mVersions := stats.Int64("versions", "Version information.", "Num")
	compiler, _ := tag.NewKey("compiler")
	goarch, _ := tag.NewKey("goarch")
	goos, _ := tag.NewKey("goos")
	goversion, _ := tag.NewKey("goversion")
	livepeerversion, _ := tag.NewKey("livepeerversion")
	ctx, err := tag.New(context.Background(), tag.Insert(census.kNodeType, nodeType), tag.Insert(census.kNodeID, nodeID),
		tag.Insert(compiler, runtime.Compiler), tag.Insert(goarch, runtime.GOARCH), tag.Insert(goos, runtime.GOOS),
		tag.Insert(goversion, runtime.Version()), tag.Insert(livepeerversion, version))
	if err != nil {
		glog.Fatal("Error creating tagged context", err)
	}
	baseTags := []tag.Key{census.kNodeID, census.kNodeType}
	views := []*view.View{
		&view.View{
			Name:        "versions",
			Measure:     mVersions,
			Description: "Versions used by LivePeer node.",
			TagKeys:     []tag.Key{census.kNodeType, compiler, goos, goversion, livepeerversion},
			Aggregation: view.LastValue(),
		},
		&view.View{
			Name:        "broadcast_client_start_failed_total",
			Measure:     census.mStartBroadcastClientFailed,
			Description: "StartBroadcastClientFailed",
			TagKeys:     baseTags,
			Aggregation: view.Count(),
		},
		&view.View{
			Name:        "stream_created_total",
			Measure:     census.mStreamCreated,
			Description: "StreamCreated",
			TagKeys:     baseTags,
			Aggregation: view.Count(),
		},
		&view.View{
			Name:        "stream_started_total",
			Measure:     census.mStreamStarted,
			Description: "StreamStarted",
			TagKeys:     baseTags,
			Aggregation: view.Count(),
		},
		&view.View{
			Name:        "stream_ended_total",
			Measure:     census.mStreamEnded,
			Description: "StreamEnded",
			TagKeys:     baseTags,
			Aggregation: view.Count(),
		},
		&view.View{
			Name:        "stream_create_failed_total",
			Measure:     census.mStreamCreateFailed,
			Description: "StreamCreateFailed",
			TagKeys:     baseTags,
			Aggregation: view.Count(),
		},
		&view.View{
			Name:        "segment_source_appeared_total",
			Measure:     census.mSegmentSourceAppeared,
			Description: "SegmentSourceAppeared",
			TagKeys:     append([]tag.Key{census.kProfile}, baseTags...),
			Aggregation: view.Count(),
		},
		&view.View{
			Name:        "segment_source_emerged_total",
			Measure:     census.mSegmentEmerged,
			Description: "SegmentEmerged",
			TagKeys:     baseTags,
			Aggregation: view.Count(),
		},
		&view.View{
			Name:        "segment_source_emerged_with_profiles_total",
			Measure:     census.mSegmentEmergedWithProfiles,
			Description: "SegmentEmerged, counted by number of transcode profiles",
			TagKeys:     baseTags,
			Aggregation: view.Count(),
		},
		&view.View{
			Name:        "segment_source_uploaded_total",
			Measure:     census.mSegmentUploaded,
			Description: "SegmentUploaded",
			TagKeys:     baseTags,
			Aggregation: view.Count(),
		},
		&view.View{
			Name:        "segment_source_upload_failed_total",
			Measure:     census.mSegmentUploadFailed,
			Description: "SegmentUploadedFailed",
			TagKeys:     append([]tag.Key{census.kErrorCode}, baseTags...),
			Aggregation: view.Count(),
		},
		&view.View{
			Name:        "segment_transcoded_total",
			Measure:     census.mSegmentTranscoded,
			Description: "SegmentTranscoded",
			TagKeys:     append([]tag.Key{census.kProfiles}, baseTags...),
			Aggregation: view.Count(),
		},
		&view.View{
			Name:        "segment_transcode_failed_total",
			Measure:     census.mSegmentTranscodeFailed,
			Description: "SegmentTranscodeFailed",
			TagKeys:     append([]tag.Key{census.kErrorCode}, baseTags...),
			Aggregation: view.Count(),
		},
		&view.View{
			Name:        "segment_transcoded_appeared_total",
			Measure:     census.mSegmentTranscodedAppeared,
			Description: "SegmentTranscodedAppeared",
			TagKeys:     append([]tag.Key{census.kProfile}, baseTags...),
			Aggregation: view.Count(),
		},
		&view.View{
			Name:        "segment_transcoded_all_appeared_total",
			Measure:     census.mSegmentTranscodedAllAppeared,
			Description: "SegmentTranscodedAllAppeared",
			TagKeys:     append([]tag.Key{census.kProfiles}, baseTags...),
			Aggregation: view.Count(),
		},
		&view.View{
			Name:        "success_rate",
			Measure:     census.mSuccessRate,
			Description: "Number of transcoded segments divided on number of source segments",
			TagKeys:     baseTags,
			Aggregation: view.LastValue(),
		},
		&view.View{
			Name:        "transcode_time_seconds",
			Measure:     census.mTranscodeTime,
			Description: "TranscodeTime, seconds",
			TagKeys:     append([]tag.Key{census.kProfiles}, baseTags...),
			Aggregation: view.Distribution(0, .250, .500, .750, 1.000, 1.250, 1.500, 2.000, 2.500, 3.000, 3.500, 4.000, 4.500, 5.000, 10.000),
		},
		&view.View{
			Name:        "transcode_latency_seconds",
			Measure:     census.mTranscodeLatency,
			Description: "Transcoding latency, from source segment emered from segmenter till transcoded segment apeeared in manifest",
			TagKeys:     append([]tag.Key{census.kProfile}, baseTags...),
			Aggregation: view.Distribution(0, .500, .75, 1.000, 1.500, 2.000, 2.500, 3.000, 3.500, 4.000, 4.500, 5.000, 10.000),
		},
		&view.View{
			Name:        "transcode_overall_latency_seconds",
			Measure:     census.mTranscodeOverallLatency,
			Description: "Transcoding latency, from source segment emered from segmenter till all transcoded segment apeeared in manifest",
			TagKeys:     append([]tag.Key{census.kProfiles}, baseTags...),
			Aggregation: view.Distribution(0, .500, .75, 1.000, 1.500, 2.000, 2.500, 3.000, 3.500, 4.000, 4.500, 5.000, 10.000),
		},
		&view.View{
			Name:        "upload_time_seconds",
			Measure:     census.mUploadTime,
			Description: "UploadTime, seconds",
			TagKeys:     baseTags,
			Aggregation: view.Distribution(0, .10, .20, .50, .100, .150, .200, .500, .1000, .5000, 10.000),
		},
		&view.View{
			Name:        "max_sessions_total",
			Measure:     census.mMaxSessions,
			Description: "Max Sessions",
			TagKeys:     baseTags,
			Aggregation: view.LastValue(),
		},
		&view.View{
			Name:        "current_sessions_total",
			Measure:     census.mCurrentSessions,
			Description: "Number of streams currently transcoding",
			TagKeys:     baseTags,
			Aggregation: view.LastValue(),
		},
		&view.View{
			Name:        "discovery_errors_total",
			Measure:     census.mDiscoveryError,
			Description: "Number of discover errors",
			TagKeys:     append([]tag.Key{census.kErrorCode}, baseTags...),
			Aggregation: view.Count(),
		},
	}
	// Register the views
	if err := view.Register(views...); err != nil {
		glog.Fatalf("Failed to register views: %v", err)
	}
	registry := rprom.NewRegistry()
	registry.MustRegister(rprom.NewProcessCollector(rprom.ProcessCollectorOpts{}))
	registry.MustRegister(rprom.NewGoCollector())
	pe, err := prometheus.NewExporter(prometheus.Options{
		Namespace: "livepeer",
		Registry:  registry,
	})
	if err != nil {
		glog.Fatalf("Failed to create the Prometheus stats exporter: %v", err)
	}

	// Register the Prometheus exporters as a stats exporter.
	view.RegisterExporter(pe)
	stats.Record(ctx, mVersions.M(1))
	ctx, err = tag.New(census.ctx, tag.Insert(census.kErrorCode, "LostSegment"))
	if err != nil {
		glog.Fatal("Error creating context", err)
	}
	if !testing {
		go census.timeoutWatcher(ctx)
	}
	Exporter = pe
}

// LogDiscoveryError records discovery error
func LogDiscoveryError(code string) {
	glog.Error("Discovery error=" + code)
	if strings.Contains(code, "OrchestratorCapped") {
		code = "OrchestratorCapped"
	} else if strings.Contains(code, "Canceled") {
		code = "Canceled"
	}
	ctx, err := tag.New(census.ctx, tag.Insert(census.kErrorCode, code))
	if err != nil {
		glog.Error("Error creating context", err)
		return
	}
	stats.Record(ctx, census.mDiscoveryError.M(1))
}

func (cen *censusMetricsCounter) successRate() float64 {
	var i int
	var f float64
	if len(cen.success) == 0 {
		return 1
	}
	for _, avg := range cen.success {
		if r, has := avg.successRate(); has {
			i++
			f += r
		}
	}
	if i > 0 {
		return f / float64(i)
	}
	return 1
}

func (sa *segmentsAverager) successRate() (float64, bool) {
	var emerged, transcoded int
	if sa.end == -1 {
		return 1, false
	}
	i := sa.start
	now := time.Now()
	for {
		item := &sa.segments[i]
		if item.transcoded > 0 || item.failed || now.Sub(item.emergedTime) > timeToWaitForError {
			emerged += item.emerged
			transcoded += item.transcoded
		}
		if i == sa.end {
			break
		}
		i = sa.advance(i)
	}
	if emerged > 0 {
		return float64(transcoded) / float64(emerged), true
	}
	return 1, false
}

func (sa *segmentsAverager) advance(i int) int {
	i++
	if i == len(sa.segments) {
		i = 0
	}
	return i
}

func (sa *segmentsAverager) addEmerged(seqNo uint64) {
	item, _ := sa.getAddItem(seqNo)
	item.emerged = 1
	item.transcoded = 0
	item.emergedTime = time.Now()
	item.seqNo = seqNo
}

func (sa *segmentsAverager) addTranscoded(seqNo uint64, failed bool) {
	item, found := sa.getAddItem(seqNo)
	if !found {
		item.emerged = 0
		item.emergedTime = time.Now()
	}
	item.failed = failed
	if !failed {
		item.transcoded = 1
	}
	item.seqNo = seqNo
}

func (sa *segmentsAverager) getAddItem(seqNo uint64) (*segmentCount, bool) {
	var index int
	if sa.end == -1 {
		sa.end = 0
	} else {
		i := sa.start
		for {
			if sa.segments[i].seqNo == seqNo {
				return &sa.segments[i], true
			}
			if i == sa.end {
				break
			}
			i = sa.advance(i)
		}
		sa.end = sa.advance(sa.end)
		index = sa.end
		if sa.end == sa.start {
			sa.start = sa.advance(sa.start)
		}
	}
	return &sa.segments[index], false
}

func (sa *segmentsAverager) canBeRemoved() bool {
	if sa.end == -1 {
		return true
	}
	i := sa.start
	now := time.Now()
	for {
		item := &sa.segments[i]
		if item.transcoded == 0 && !item.failed && now.Sub(item.emergedTime) <= timeToWaitForError {
			return false
		}
		if i == sa.end {
			break
		}
		i = sa.advance(i)
	}
	return true
}

func (cen *censusMetricsCounter) timeoutWatcher(ctx context.Context) {
	for {
		cen.lock.Lock()
		now := time.Now()
		for nonce, emerged := range cen.emergeTimes {
			for seqNo, tm := range emerged {
				ago := now.Sub(tm)
				if ago > timeToWaitForError {
					stats.Record(cen.ctx, cen.mSegmentEmerged.M(1))
					delete(emerged, seqNo)
					// This shouldn't happen, but if it is, we record
					// `LostSegment` error, to try to find out why we missed segment
					stats.Record(ctx, cen.mSegmentTranscodeFailed.M(1))
					glog.Errorf("LostSegment nonce=%d seqNo=%d emerged=%ss ago", nonce, seqNo, ago)
				}
			}
		}
		cen.sendSuccess()
		for nonce, avg := range cen.success {
			if avg.removed && now.Sub(avg.removedAt) > 2*timeToWaitForError {
				// need to keep this around for some time to give Prometheus chance to scrape this value
				// (Prometheus scrapes every 5 seconds)
				delete(cen.success, nonce)
			}
		}
		cen.lock.Unlock()
		time.Sleep(timeoutWatcherPause)
	}
}

func MaxSessions(maxSessions int) {
	census.lock.Lock()
	defer census.lock.Unlock()
	stats.Record(census.ctx, census.mMaxSessions.M(int64(maxSessions)))
}

func CurrentSessions(currentSessions int) {
	census.lock.Lock()
	defer census.lock.Unlock()
	stats.Record(census.ctx, census.mCurrentSessions.M(int64(currentSessions)))
}

func (cen *censusMetricsCounter) segmentEmerged(nonce, seqNo uint64, profilesNum int) {
	cen.lock.Lock()
	defer cen.lock.Unlock()
	if _, has := cen.emergeTimes[nonce]; !has {
		cen.emergeTimes[nonce] = make(map[uint64]time.Time)
	}
	if avg, has := cen.success[nonce]; has {
		avg.addEmerged(seqNo)
	}
	cen.emergeTimes[nonce][seqNo] = time.Now()
}

func (cen *censusMetricsCounter) segmentSourceAppeared(nonce, seqNo uint64, profile string) {
	cen.lock.Lock()
	defer cen.lock.Unlock()
	ctx, err := tag.New(cen.ctx, tag.Insert(census.kProfile, profile))
	if err != nil {
		glog.Error("Error creating context", err)
		return
	}
	stats.Record(ctx, cen.mSegmentSourceAppeared.M(1))
}

func (cen *censusMetricsCounter) segmentUploaded(nonce, seqNo uint64, uploadDur time.Duration) {
	cen.lock.Lock()
	defer cen.lock.Unlock()
	stats.Record(cen.ctx, cen.mSegmentUploaded.M(1), cen.mUploadTime.M(float64(uploadDur/time.Second)))
}

func (cen *censusMetricsCounter) segmentUploadFailed(nonce, seqNo uint64, code SegmentUploadError) {
	cen.lock.Lock()
	defer cen.lock.Unlock()
	cen.countSegmentEmerged(nonce, seqNo)

	ctx, err := tag.New(cen.ctx, tag.Insert(census.kErrorCode, string(code)))
	if err != nil {
		glog.Error("Error creating context", err)
		return
	}
	stats.Record(ctx, cen.mSegmentUploadFailed.M(1))
	cen.countSegmentTranscoded(nonce, seqNo, true)
	cen.sendSuccess()
}

func (cen *censusMetricsCounter) segmentTranscoded(nonce, seqNo uint64, transcodeDur, totalDur time.Duration,
	profiles string) {
	cen.lock.Lock()
	defer cen.lock.Unlock()
	ctx, err := tag.New(cen.ctx, tag.Insert(cen.kProfiles, profiles))
	if err != nil {
		glog.Error("Error creating context", err)
		return
	}
	stats.Record(ctx, cen.mSegmentTranscoded.M(1), cen.mTranscodeTime.M(float64(transcodeDur/time.Second)))
}

func (cen *censusMetricsCounter) segmentTranscodeFailed(nonce, seqNo uint64, code SegmentTranscodeError) {
	cen.lock.Lock()
	defer cen.lock.Unlock()
	ctx, err := tag.New(cen.ctx, tag.Insert(census.kErrorCode, string(code)))
	if err != nil {
		glog.Error("Error creating context", err)
		return
	}
	stats.Record(ctx, cen.mSegmentTranscodeFailed.M(1))
	cen.countSegmentEmerged(nonce, seqNo)
	cen.countSegmentTranscoded(nonce, seqNo, code != SegmentTranscodeErrorSessionEnded)
	cen.sendSuccess()
}

func (cen *censusMetricsCounter) countSegmentTranscoded(nonce, seqNo uint64, failed bool) {
	if avg, ok := cen.success[nonce]; ok {
		avg.addTranscoded(seqNo, failed)
	}
}

func (cen *censusMetricsCounter) countSegmentEmerged(nonce, seqNo uint64) {
	if _, ok := cen.emergeTimes[nonce][seqNo]; ok {
		stats.Record(cen.ctx, cen.mSegmentEmerged.M(1))
		delete(cen.emergeTimes[nonce], seqNo)
	}
}

func (cen *censusMetricsCounter) sendSuccess() {
	stats.Record(cen.ctx, cen.mSuccessRate.M(cen.successRate()))
}

func SegmentFullyTranscoded(nonce, seqNo uint64, profiles string, allSuccess bool, errCode SegmentTranscodeError) {
	census.lock.Lock()
	defer census.lock.Unlock()
	ctx, err := tag.New(census.ctx, tag.Insert(census.kProfiles, profiles))
	if err != nil {
		glog.Error("Error creating context", err)
		return
	}

	if st, ok := census.emergeTimes[nonce][seqNo]; ok {
		if allSuccess {
			latency := time.Since(st)
			stats.Record(ctx, census.mTranscodeOverallLatency.M(float64(latency/time.Second)))
		}
		census.countSegmentEmerged(nonce, seqNo)
	}
	if allSuccess {
		stats.Record(ctx, census.mSegmentTranscodedAllAppeared.M(1))
	}
	census.countSegmentTranscoded(nonce, seqNo, !allSuccess && errCode != SegmentTranscodeErrorSessionEnded)
	census.sendSuccess()
}

func (cen *censusMetricsCounter) segmentTranscodedAppeared(nonce, seqNo uint64, profile string) {
	cen.lock.Lock()
	defer cen.lock.Unlock()
	ctx, err := tag.New(cen.ctx, tag.Insert(cen.kProfile, profile))
	if err != nil {
		glog.Error("Error creating context", err)
		return
	}

	// cen.transcodedSegments[nonce] = cen.transcodedSegments[nonce] + 1
	if st, ok := cen.emergeTimes[nonce][seqNo]; ok {
		latency := time.Since(st)
		stats.Record(ctx, cen.mTranscodeLatency.M(float64(latency/time.Second)))
	}

	stats.Record(ctx, cen.mSegmentTranscodedAppeared.M(1))
}

func (cen *censusMetricsCounter) streamCreateFailed(nonce uint64, reason string) {
	cen.lock.Lock()
	defer cen.lock.Unlock()
	stats.Record(cen.ctx, cen.mStreamCreateFailed.M(1))
}

func newAverager() *segmentsAverager {
	return &segmentsAverager{
		segments: make([]segmentCount, numberOfSegmentsToCalcAverage),
		end:      -1,
	}
}

func (cen *censusMetricsCounter) streamCreated(nonce uint64) {
	cen.lock.Lock()
	defer cen.lock.Unlock()
	stats.Record(cen.ctx, cen.mStreamCreated.M(1))
	cen.success[nonce] = newAverager()
}

func (cen *censusMetricsCounter) streamStarted(nonce uint64) {
	cen.lock.Lock()
	defer cen.lock.Unlock()
	stats.Record(cen.ctx, cen.mStreamStarted.M(1))
}

func (cen *censusMetricsCounter) streamEnded(nonce uint64) {
	cen.lock.Lock()
	defer cen.lock.Unlock()
	stats.Record(cen.ctx, cen.mStreamEnded.M(1))
	delete(cen.emergeTimes, nonce)
	if avg, has := cen.success[nonce]; has {
		if avg.canBeRemoved() {
			delete(cen.success, nonce)
		} else {
			avg.removed = true
			avg.removedAt = time.Now()
		}
	}
	census.sendSuccess()
}
