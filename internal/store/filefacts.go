package store

import "database/sql"

// What Library stats group files by, kept out of files.info's JSON so the
// stats are plain GROUP BYs: parsing every file's stream list on each load
// took ~300 ms for 500 files on a Pi 4 (#110).
//
//   - files.vdepth: the bit depth of the first video stream (0 = unknown)
//   - file_audio: one row per distinct audio codec in a file
//
// Triggers keep them in step with files.info on every write, whatever the
// write path; migration 3 fills them in for files probed before.
var fileFactsSchema = `
CREATE TABLE IF NOT EXISTS file_audio (
	file_id INTEGER NOT NULL,
	codec TEXT NOT NULL,
	PRIMARY KEY (file_id, codec)
) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS file_audio_codec ON file_audio(codec);

CREATE TRIGGER IF NOT EXISTS files_facts_insert AFTER INSERT ON files BEGIN
	` + fileFactsSync + `
END;
CREATE TRIGGER IF NOT EXISTS files_facts_update AFTER UPDATE OF info ON files BEGIN
	DELETE FROM file_audio WHERE file_id = NEW.id;
	` + fileFactsSync + `
END;
CREATE TRIGGER IF NOT EXISTS files_facts_delete AFTER DELETE ON files BEGIN
	DELETE FROM file_audio WHERE file_id = OLD.id;
END;
`

// fileFactsSync derives the facts of row NEW from its info.
var fileFactsSync = `UPDATE files SET vdepth = ` + vdepthOf("NEW.info") + ` WHERE id = NEW.id;
	INSERT OR IGNORE INTO file_audio(file_id, codec)
		SELECT NEW.id, COALESCE(NULLIF(json_extract(t.value,'$.codec'),''),'unknown')
		FROM json_each(CASE WHEN json_valid(NEW.info) THEN NEW.info END,'$.streams') t
		WHERE json_extract(t.value,'$.type')='audio';`

func vdepthOf(info string) string {
	return `COALESCE((SELECT json_extract(t.value,'$.bitDepth') FROM json_each(CASE WHEN json_valid(` + info + `) THEN ` + info + ` END,'$.streams') t
		WHERE json_extract(t.value,'$.type')='video' ORDER BY t.key LIMIT 1), 0)`
}

// backfillFileFacts is migration 3: the facts of every file probed before.
func backfillFileFacts(tx *sql.Tx) error {
	if _, err := tx.Exec(`UPDATE files SET vdepth = ` + vdepthOf("info") + ` WHERE info != ''`); err != nil {
		return err
	}
	_, err := tx.Exec(`DELETE FROM file_audio;
		INSERT OR IGNORE INTO file_audio(file_id, codec)
		SELECT f.id, COALESCE(NULLIF(json_extract(t.value,'$.codec'),''),'unknown')
		FROM files f, json_each(f.info,'$.streams') t
		WHERE json_valid(f.info) AND json_extract(t.value,'$.type')='audio'`)
	return err
}
