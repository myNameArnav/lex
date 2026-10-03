package store

import (
	"context"
	"database/sql"
	"errors"
)

// Stream describes one elementary stream of a media file.
type Stream struct {
	Index         int     `json:"index"`
	Type          string  `json:"type"` // video | audio | subtitle
	Codec         string  `json:"codec"`
	Profile       string  `json:"profile,omitempty"`
	Level         int     `json:"level,omitempty"`
	PixFmt        string  `json:"pixFmt,omitempty"`
	BitDepth      int     `json:"bitDepth,omitempty"`
	Width         int     `json:"width,omitempty"`
	Height        int     `json:"height,omitempty"`
	FrameRate     float64 `json:"frameRate,omitempty"`
	HDR           string  `json:"hdr,omitempty"`
	DVProfile     int     `json:"dvProfile,omitempty"`
	DVCompat      int     `json:"dvCompat,omitempty"`
	Channels      int     `json:"channels,omitempty"`
	ChannelLayout string  `json:"channelLayout,omitempty"`
	SampleRate    int     `json:"sampleRate,omitempty"`
	Bitrate       int64   `json:"bitrate,omitempty"`
	Language      string  `json:"language,omitempty"`
	Title         string  `json:"title,omitempty"`
	Default       bool    `json:"default,omitempty"`
	Forced        bool    `json:"forced,omitempty"`
	TextSub       bool    `json:"textSub,omitempty"`
	External      bool    `json:"external,omitempty"`
	Downloaded    bool    `json:"downloaded,omitempty"`
	DownloadedBy  int64   `json:"downloadedBy,omitempty"` // user who downloaded it; they or an admin may remove it
	ExternalPath  string  `json:"-"`
	CodecString   string  `json:"codecString,omitempty"` // RFC 6381 codec id for MSE
}

// MediaInfo is the probe summary stored per file.
type MediaInfo struct {
	Format    string    `json:"format"`
	Duration  float64   `json:"duration"`
	StartTime float64   `json:"startTime"`
	Bitrate   int64     `json:"bitrate"`
	Streams   []Stream  `json:"streams"`
	Chapters  []Chapter `json:"chapters,omitempty"`
}

type Chapter struct {
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	Title string  `json:"title,omitempty"`
}

func (m *MediaInfo) Video() *Stream {
	for i := range m.Streams {
		if m.Streams[i].Type == "video" {
			return &m.Streams[i]
		}
	}
	return nil
}

func (m *MediaInfo) StreamByIndex(idx int) *Stream {
	for i := range m.Streams {
		if m.Streams[i].Index == idx {
			return &m.Streams[i]
		}
	}
	return nil
}

func (m *MediaInfo) Audio() []Stream {
	var out []Stream
	for _, s := range m.Streams {
		if s.Type == "audio" {
			out = append(out, s)
		}
	}
	return out
}

type File struct {
	ID         int64      `json:"id"`
	ItemID     int64      `json:"itemId"`
	LibraryID  int64      `json:"libraryId"`
	Path       string     `json:"path"`
	Size       int64      `json:"size"`
	Mtime      int64      `json:"mtime"`
	Container  string     `json:"container"`
	Duration   float64    `json:"duration"`
	Bitrate    int64      `json:"bitrate"`
	Width      int        `json:"width"`
	Height     int        `json:"height"`
	VCodec     string     `json:"vcodec"`
	ACodec     string     `json:"acodec"`
	HDR        string     `json:"hdr,omitempty"`
	Info       *MediaInfo `json:"info,omitempty"`
	ProbedAt   int64      `json:"probedAt"`
	ProbeError string     `json:"probeError,omitempty"`
	AddedAt    int64      `json:"addedAt"`
}

const fileCols = `id,item_id,library_id,path,size,mtime,container,duration,bitrate,width,height,vcodec,acodec,hdr,info,probed_at,probe_error,added_at`

func scanFile(r scanner) (*File, error) {
	f := &File{}
	var info string
	if err := r.Scan(&f.ID, &f.ItemID, &f.LibraryID, &f.Path, &f.Size, &f.Mtime, &f.Container, &f.Duration, &f.Bitrate, &f.Width, &f.Height,
		&f.VCodec, &f.ACodec, &f.HDR, &info, &f.ProbedAt, &f.ProbeError, &f.AddedAt); err != nil {
		return nil, err
	}
	if info != "" {
		f.Info = &MediaInfo{}
		jsonInto(info, f.Info)
	}
	return f, nil
}

func (s *Store) queryFiles(q string, args ...any) ([]*File, error) {
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*File
	for rows.Next() {
		f, err := scanFile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func (s *Store) File(id int64) (*File, error) {
	f, err := scanFile(s.db.QueryRow(`SELECT `+fileCols+` FROM files WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return f, err
}

func (s *Store) ItemFiles(itemID int64) ([]*File, error) {
	return s.queryFiles(`SELECT `+fileCols+` FROM files WHERE item_id=? ORDER BY height DESC, size DESC`, itemID)
}

func (s *Store) LibraryFiles(libID int64) ([]*File, error) {
	return s.queryFiles(`SELECT `+fileCols+` FROM files WHERE library_id=?`, libID)
}

func (s *Store) AllFiles() ([]*File, error) {
	return s.queryFiles(`SELECT ` + fileCols + ` FROM files`)
}

// Failed probes are retried with a growing delay (1h, 2h, 4h, ... up to a
// week): a network share timing out once shouldn't leave a file without
// duration and codecs forever, but a corrupt file mustn't be probed in a loop.
const (
	probeRetryBase = 3600
	probeRetryMax  = 7 * 24 * 3600
)

// FilesNeedingProbe returns files never probed (or changed since) and files
// whose last probe failed and whose retry delay has passed.
func (s *Store) FilesNeedingProbe() ([]*File, error) {
	return s.queryFiles(`SELECT `+fileCols+` FROM files WHERE probed_at=0
		OR (probe_error<>'' AND probed_at <= ? - MIN(?, ? << MIN(MAX(probe_attempts-1, 0), 20)))
		ORDER BY added_at DESC`, now(), probeRetryMax, probeRetryBase)
}

func (s *Store) InsertFile(f *File) (int64, error) {
	if f.AddedAt == 0 {
		f.AddedAt = now()
	}
	res, err := s.db.Exec(`INSERT INTO files(item_id,library_id,path,size,mtime,added_at) VALUES(?,?,?,?,?,?)`,
		f.ItemID, f.LibraryID, f.Path, f.Size, f.Mtime, f.AddedAt)
	if err != nil {
		return 0, err
	}
	f.ID, _ = res.LastInsertId()
	return f.ID, nil
}

// MarkFileChanged records a new size/mtime and queues a re-probe.
func (s *Store) MarkFileChanged(id, size, mtime int64) error {
	_, err := s.db.Exec(`UPDATE files SET size=?,mtime=?,probed_at=0,probe_attempts=0 WHERE id=?`, size, mtime, id)
	return err
}

// MoveFile re-points a file at another item (its name or folder now parses
// differently) and carries the old item's watch state, intro segments,
// history and manual match over, so they survive the old item being pruned.
func (s *Store) MoveFile(id, oldItem, newItem int64) error {
	return s.Tx(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(`UPDATE files SET item_id=? WHERE id=?`, newItem, id); err != nil {
			return err
		}
		if oldItem == newItem {
			return nil
		}
		return carryOver(tx, id, oldItem, newItem)
	})
}

func (s *Store) SaveProbe(id int64, info *MediaInfo, probeErr string) error {
	if info == nil {
		_, err := s.db.Exec(`UPDATE files SET probed_at=?,probe_error=?,probe_attempts=probe_attempts+1 WHERE id=?`, now(), probeErr, id)
		return err
	}
	var w, h int
	var vc, ac, hdr string
	if v := info.Video(); v != nil {
		w, h, vc, hdr = v.Width, v.Height, v.Codec, v.HDR
	}
	for _, a := range info.Audio() {
		if a.Default || ac == "" {
			ac = a.Codec
			if a.Default {
				break
			}
		}
	}
	_, err := s.db.Exec(`UPDATE files SET container=?,duration=?,bitrate=?,width=?,height=?,vcodec=?,acodec=?,hdr=?,info=?,probed_at=?,probe_error='',probe_attempts=0 WHERE id=?`,
		info.Format, info.Duration, info.Bitrate, w, h, vc, ac, hdr, jsonString(info), now(), id)
	return err
}

// RelocateFile records that a file was moved/renamed, keeping its probe
// data, and carries watch state, intro segments, history and a manual match
// over to the new item when the file changed item.
func (s *Store) RelocateFile(id int64, newPath string, oldItem, newItem int64) error {
	return s.Tx(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(`UPDATE files SET path=?, item_id=? WHERE id=?`, newPath, newItem, id); err != nil {
			return err
		}
		if oldItem == newItem {
			return nil
		}
		return carryOver(tx, id, oldItem, newItem)
	})
}

// carryOver copies what users and the matcher attached to oldItem onto
// newItem after file fileID moved between them. Watch state is merged (the
// most recent position wins, played/favorite stick); history of the moved
// file follows it, and all history does once oldItem has no files left.
// A movie's manual match (or a match the new item lacks) is copied too.
func carryOver(tx *sql.Tx, fileID, oldItem, newItem int64) error {
	stmts := []struct {
		q    string
		args []any
	}{
		{`INSERT INTO user_data(user_id,item_id,position,played,play_count,last_played,favorite)
			SELECT user_id,?,position,played,play_count,last_played,favorite FROM user_data WHERE item_id=?
			ON CONFLICT(user_id,item_id) DO UPDATE SET
				position=CASE WHEN excluded.last_played>user_data.last_played THEN excluded.position ELSE user_data.position END,
				last_played=MAX(user_data.last_played,excluded.last_played),
				played=MAX(user_data.played,excluded.played),
				play_count=MAX(user_data.play_count,excluded.play_count),
				favorite=MAX(user_data.favorite,excluded.favorite)`, []any{newItem, oldItem}},
		{`INSERT OR IGNORE INTO segments(item_id,kind,start,end,source) SELECT ?,kind,start,end,source FROM segments WHERE item_id=?`, []any{newItem, oldItem}},
		{`UPDATE history SET item_id=? WHERE item_id=? AND (file_id=? OR NOT EXISTS (SELECT 1 FROM files WHERE item_id=?))`, []any{newItem, oldItem, fileID, oldItem}},
		{`UPDATE items SET title=o.title,sort_title=o.sort_title,original_title=o.original_title,year=o.year,overview=o.overview,tagline=o.tagline,
				rating=o.rating,content_rating=o.content_rating,genres=o.genres,cast_json=o.cast_json,studios=o.studios,runtime=o.runtime,
				premiere=o.premiere,poster=o.poster,backdrop=o.backdrop,thumb=o.thumb,provider_ids=o.provider_ids,
				meta_status=o.meta_status,meta_locked=o.meta_locked,updated_at=?
			FROM (SELECT * FROM items WHERE id=?) AS o
			WHERE items.id=? AND items.kind='movie' AND o.kind='movie' AND items.meta_locked=0
				AND (o.meta_locked=1 OR (o.meta_status=? AND items.meta_status<>?))`,
			[]any{now(), oldItem, newItem, MetaMatched, MetaMatched}},
		// Keep the movie where it was in "recently added".
		{`UPDATE items SET added_at=MIN(added_at,(SELECT added_at FROM items WHERE id=?)) WHERE id=? AND kind='movie'`, []any{oldItem, newItem}},
	}
	for _, st := range stmts {
		if _, err := tx.Exec(st.q, st.args...); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) DeleteFile(id int64) error {
	_, err := s.db.Exec(`DELETE FROM files WHERE id=?`, id)
	return err
}

// ---- history ----

type HistoryEntry struct {
	ID            int64   `json:"id"`
	UserID        int64   `json:"userId"`
	UserName      string  `json:"userName"`
	ItemID        int64   `json:"itemId"`
	Title         string  `json:"title"`
	FileID        int64   `json:"fileId"`
	StartedAt     int64   `json:"startedAt"`
	EndedAt       int64   `json:"endedAt"`
	Watched       float64 `json:"watched"`
	Method        string  `json:"method"`
	Reasons       string  `json:"reasons"`
	Client        string  `json:"client"`
	IP            string  `json:"ip"`
	Remote        bool    `json:"remote"`
	Bytes         int64   `json:"bytes"`
	VideoOut      string  `json:"videoOut"`
	AudioOut      string  `json:"audioOut"`
	BufferEvents  int     `json:"bufferEvents"`
	BufferSeconds float64 `json:"bufferSeconds"`
}

func (s *Store) AddHistory(h *HistoryEntry) error {
	_, err := s.db.Exec(`INSERT INTO history(user_id,user_name,item_id,title,file_id,started_at,ended_at,watched,method,reasons,client,ip,remote,bytes,video_out,audio_out,buffer_events,buffer_seconds)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		h.UserID, h.UserName, h.ItemID, h.Title, h.FileID, h.StartedAt, h.EndedAt, h.Watched, h.Method, h.Reasons, h.Client, h.IP, h.Remote, h.Bytes,
		h.VideoOut, h.AudioOut, h.BufferEvents, h.BufferSeconds)
	return err
}

func (s *Store) History(limit, offset int) ([]HistoryEntry, error) {
	rows, err := s.db.Query(`SELECT id,user_id,user_name,item_id,title,file_id,started_at,ended_at,watched,method,reasons,client,ip,remote,bytes,video_out,audio_out,buffer_events,buffer_seconds
		FROM history ORDER BY started_at DESC LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HistoryEntry
	for rows.Next() {
		var h HistoryEntry
		if err := rows.Scan(&h.ID, &h.UserID, &h.UserName, &h.ItemID, &h.Title, &h.FileID, &h.StartedAt, &h.EndedAt, &h.Watched, &h.Method, &h.Reasons,
			&h.Client, &h.IP, &h.Remote, &h.Bytes, &h.VideoOut, &h.AudioOut, &h.BufferEvents, &h.BufferSeconds); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

func (s *Store) ClearHistory() error {
	_, err := s.db.Exec(`DELETE FROM history`)
	return err
}
