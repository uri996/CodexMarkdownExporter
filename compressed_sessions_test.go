package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
)

func writeSyntheticZstd(t *testing.T, path string, data []byte) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	encoder, err := zstd.NewWriter(f, zstd.WithEncoderConcurrency(1), zstd.WithWindowSize(1<<20))
	if err != nil {
		f.Close()
		t.Fatal(err)
	}
	_, writeErr := encoder.Write(data)
	if err := errors.Join(writeErr, encoder.Close(), f.Close()); err != nil {
		t.Fatal(err)
	}
}

func compressSyntheticSession(t *testing.T, s sessionSummary, removeRaw bool) sessionSummary {
	t.Helper()
	data, err := os.ReadFile(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	writeSyntheticZstd(t, s.Path+".zst", data)
	if removeRaw {
		if err := os.Remove(s.Path); err != nil {
			t.Fatal(err)
		}
	}
	s.Path += ".zst"
	return s
}

func TestCompressedReadOnlyAndLegacyPath(t *testing.T) {
	_, project := projectFixture(t)
	original := project.Sessions[0]
	want, _, err := renderSession(original, false)
	if err != nil {
		t.Fatal(err)
	}
	wantHash, err := fileFingerprint(original.Path)
	if err != nil {
		t.Fatal(err)
	}
	beforeSummary, _, err := readSummaryFromRollout(original.Path)
	if err != nil {
		t.Fatal(err)
	}
	compressed := compressSyntheticSession(t, original, true)
	before, err := os.ReadFile(compressed.Path)
	if err != nil {
		t.Fatal(err)
	}
	summary, ok, err := readSummaryFromRollout(original.Path)
	if err != nil || !ok || summary.Path != compressed.Path || summary.SessionID != original.SessionID || !summary.CreatedAt.Equal(beforeSummary.CreatedAt) {
		t.Fatalf("old .jsonl reference not resolved: %v", err)
	}
	got, _, err := renderSession(original, false)
	if err != nil || got != want {
		t.Fatalf("compressed body changed: %v", err)
	}
	if key := sessionSourceKey(compressed); key != sessionSourceKey(original) {
		t.Fatal("format changed stable identity")
	}
	if digest, err := fileFingerprint(compressed.Path); err != nil || digest != wantHash {
		t.Fatal("hash must describe decoded source bytes")
	}
	after, _ := os.ReadFile(compressed.Path)
	if !bytes.Equal(before, after) {
		t.Fatal("source compressed file was modified")
	}
	if _, err := os.Stat(original.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("reader extracted the original file")
	}
	if err := os.Remove(compressed.Path); err != nil {
		t.Fatal("decoder/file handle leaked", err)
	}
	if _, _, err := collectConversation(original.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("missing source not reported")
	}
}

func TestCompressedLargeUnicodeRecord(t *testing.T) {
	_, project := projectFixture(t)
	s := project.Sessions[0]
	raw, err := os.ReadFile(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	large := strings.Repeat("中文", 3<<20) + " # Markdown <example>正文</example>"
	raw = bytes.Replace(raw, []byte("synthetic prompt 中文"), []byte(large), 1)
	writeSyntheticZstd(t, s.Path+".zst", raw)
	s.Path += ".zst"
	_, messages, err := collectConversation(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	messages = selectMessagesForExport(messages, false)
	if len(messages) != 2 || messages[0].Body != large || messages[1].Body != "synthetic final 中文" {
		t.Fatal("large record or final filter regressed")
	}
}

func TestCompressedCorruptionAndResourceLimits(t *testing.T) {
	_, project := projectFixture(t)
	s := compressSyntheticSession(t, project.Sessions[0], true)
	valid, err := os.ReadFile(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	badCRC := append([]byte{}, valid...)
	badCRC[len(badCRC)-1] ^= 0xff
	for name, data := range map[string][]byte{"magic": []byte("not zstd"), "truncated": valid[:len(valid)-3], "checksum": badCRC, "oversized_window": {0x28, 0xb5, 0x2f, 0xfd, 0, 0x88, 1, 0, 0}} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "rollout-synthetic.jsonl.zst")
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, _, err := readSummaryFromRollout(path); err == nil || !strings.Contains(err.Error(), "zstd") {
				t.Fatal("compressed discovery corruption not reported", err)
			}
			if _, _, err := collectConversation(path); err == nil {
				t.Fatal("corrupt source rendered successfully")
			}
			if err := os.Remove(path); err != nil {
				t.Fatal("error leaked a source handle")
			}
		})
	}
	r, err := openRollout(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	r.remaining = 32
	_, err = io.Copy(io.Discard, r)
	r.Close()
	if !errors.Is(err, errRolloutTooLarge) {
		t.Fatal("decoded size limit silently truncated", err)
	}
}

func TestCompressedDiscoveryProjectsAndArchives(t *testing.T) {
	storage, project := projectFixture(t)
	compressed := compressSyntheticSession(t, project.Sessions[0], true)
	sub := syntheticSession(t, storage, "synthetic-subagent", project.Root, "subagent")
	compressSyntheticSession(t, sub, true)
	archive := makeDirectory(t, filepath.Join(filepath.Dir(storage), "archived_sessions"))
	archived := syntheticSession(t, archive, "synthetic-archived", project.Root, "cli")
	compressSyntheticSession(t, archived, true)
	found := discoverSessions(storage, true)
	if found.Rollouts != 4 || found.Interactive != 3 || len(found.Issues) != 0 {
		t.Fatalf("discovery failed: %d %d %v", found.Rollouts, found.Interactive, found.Issues)
	}
	groups, issues := groupSessionsByProject(found.Sessions)
	if len(groups) != 1 || len(issues) != 0 || len(groups[0].Sessions) != 3 {
		t.Fatal("compressed projects/archives lost")
	}
	all, err := listSessions(storage, "unrelated", true)
	if err != nil || len(all) != 3 {
		t.Fatal("single must discover compressed archives too")
	}
	var resolved bool
	for _, s := range all {
		if s.Path == compressed.Path {
			resolved = true
		}
		if s.Source == "subagent" {
			t.Fatal("subagent included")
		}
	}
	if !resolved {
		t.Fatal("compressed source missing from picker input")
	}
	singleRoot := t.TempDir()
	report := portableReport{}
	if err := runSingleExport(options{sessionsDir: storage, showAll: true, sessionID: compressed.SessionID, outputDir: singleRoot, report: &report}); err != nil {
		t.Fatal(err)
	}
	if report.Messages != 2 {
		t.Fatal("single did not render full compressed conversation")
	}
	batch := exportProjectSessions(t.TempDir(), groups[0])
	if batch.Success != 3 || batch.failed() {
		t.Fatal("mixed batch failed", batch)
	}
}

func TestIdenticalRepresentationsExportOnce(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		t.Run(map[bool]string{false: "raw_first", true: "zstd_first"}[reverse], func(t *testing.T) {
			_, project := projectFixture(t)
			raw := project.Sessions[0]
			comp := compressSyntheticSession(t, raw, false)
			project.Sessions = []sessionSummary{raw, comp}
			if reverse {
				project.Sessions = []sessionSummary{comp, raw}
			}
			root := t.TempDir()
			first := exportProjectSessions(root, project)
			if first.Success != 1 || first.Deduplicated != 1 || first.failed() {
				t.Fatal("same decoded bytes exported twice", first)
			}
			again := exportProjectSessions(root, project)
			if again.Success != 0 || again.Written != 0 || again.failed() {
				t.Fatal("repeat added output", again)
			}
			files, _ := filepath.Glob(filepath.Join(first.Directory, "*.md"))
			if len(files) != 1 {
				t.Fatal("duplicate Markdown")
			}
		})
	}
}

func TestDifferentRepresentationsRetainBoth(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		t.Run(map[bool]string{false: "raw_first", true: "zstd_first"}[reverse], func(t *testing.T) {
			_, project := projectFixture(t)
			raw := project.Sessions[0]
			data, _ := os.ReadFile(raw.Path)
			data = bytes.Replace(data, []byte("synthetic final 中文"), []byte("different compressed final 中文"), 1)
			comp := raw
			comp.Path += ".zst"
			writeSyntheticZstd(t, comp.Path, data)
			project.Sessions = []sessionSummary{raw, comp}
			if reverse {
				project.Sessions = []sessionSummary{comp, raw}
			}
			root := t.TempDir()
			first := exportProjectSessions(root, project)
			if first.Success != 2 || first.Conflicts != 1 || first.failed() {
				t.Fatal("same-name conflict lost a version", first)
			}
			again := exportProjectSessions(root, project)
			if again.Success != 0 || again.Skipped != 2 || again.failed() {
				t.Fatal("unstable conflict identity", again)
			}
			files, _ := filepath.Glob(filepath.Join(first.Directory, "*.md"))
			if len(files) != 2 {
				t.Fatal("wrong file count")
			}
			a, _ := os.ReadFile(files[0])
			b, _ := os.ReadFile(files[1])
			if bytes.Equal(a, b) {
				t.Fatal("versions were collapsed")
			}
			if err := os.Remove(raw.Path); err != nil {
				t.Fatal(err)
			}
			project.Sessions = []sessionSummary{comp}
			if r := exportProjectSessions(root, project); r.Success != 0 || r.Skipped != 1 || r.failed() {
				t.Fatal("secondary source changed identity when raw vanished", r)
			}
			writeSyntheticZstd(t, comp.Path, append(data, []byte("\n")...))
			if r := exportProjectSessions(root, project); r.Success != 0 || r.Skipped != 1 || r.failed() {
				t.Fatal("growing variant created a new export", r)
			}
		})
	}
}

func TestFormatConversionPreservesLegacyBatchIndex(t *testing.T) {
	_, project := projectFixture(t)
	raw := project.Sessions[0]
	project.Sessions = []sessionSummary{raw}
	root := t.TempDir()
	first := exportProjectSessions(root, project)
	if first.Success != 1 {
		t.Fatal(first)
	}
	indexPath := filepath.Join(root, ".codex-exporter", "index", project.Key+".json")
	index, err := readBatchIndex(indexPath, project.Key)
	if err != nil {
		t.Fatal(err)
	}
	key := sessionSourceKey(raw)
	entry := index.Entries[key]
	entry.SourceFormat = ""
	index.Entries[key] = entry
	if err := saveBatchIndex(indexPath, index); err != nil {
		t.Fatal(err)
	} // V1 schema, no format tag
	file := filepath.Join(first.Directory, entry.File)
	if err := os.WriteFile(file, []byte("hand edited synthetic output"), 0o600); err != nil {
		t.Fatal(err)
	}
	comp := compressSyntheticSession(t, raw, true)
	project.Sessions = []sessionSummary{comp}
	if r := exportProjectSessions(root, project); r.Skipped != 1 || r.Written != 0 || r.failed() {
		t.Fatal("compression invalidated legacy index", r)
	}
	if got, _ := os.ReadFile(file); string(got) != "hand edited synthetic output" {
		t.Fatal("user edits overwritten")
	}
	data, _ := os.ReadFile(comp.Path)
	decoder, err := zstd.NewReader(nil)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decoder.DecodeAll(data, nil)
	decoder.Close()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(raw.Path, decoded, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(comp.Path); err != nil {
		t.Fatal(err)
	}
	project.Sessions = []sessionSummary{raw}
	if r := exportProjectSessions(root, project); r.Skipped != 1 || r.Success != 0 || r.failed() {
		t.Fatal("reverse format conversion invalidated index", r)
	}
}

func TestCompressedFirstThenConflictingRaw(t *testing.T) {
	for _, rawFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "compressed_iteration_first", true: "raw_iteration_first"}[rawFirst], func(t *testing.T) {
			_, project := projectFixture(t)
			raw := project.Sessions[0]
			comp := compressSyntheticSession(t, raw, true)
			root := t.TempDir()
			project.Sessions = []sessionSummary{comp}
			first := exportProjectSessions(root, project)
			if first.Success != 1 {
				t.Fatal(first)
			}
			// A new different raw sibling must not be mistaken for the old snapshot.
			syntheticSession(t, filepath.Dir(raw.Path), raw.SessionID, raw.Cwd, "vscode")
			data, _ := os.ReadFile(raw.Path)
			data = bytes.Replace(data, []byte("synthetic final 中文"), []byte("new raw final"), 1)
			if err := os.WriteFile(raw.Path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			project.Sessions = []sessionSummary{comp, raw}
			if rawFirst {
				project.Sessions = []sessionSummary{raw, comp}
			}
			second := exportProjectSessions(root, project)
			if second.Success != 1 || second.Skipped != 1 || second.failed() {
				t.Fatal("compressed-first snapshot hid raw version", second)
			}
			if r := exportProjectSessions(root, project); r.Skipped != 2 || r.Success != 0 || r.failed() {
				t.Fatal("rebound index not stable", r)
			}
		})
	}
}

func TestCompressedBatchContinuesAfterReadFailure(t *testing.T) {
	storage, project := projectFixture(t)
	bad := project.Sessions[0]
	bad.Path = filepath.Join(storage, "rollout-corrupt.jsonl.zst")
	if err := os.WriteFile(bad.Path, []byte("broken compressed input"), 0o600); err != nil {
		t.Fatal(err)
	}
	project.Sessions = append([]sessionSummary{bad}, project.Sessions...)
	result := exportProjectSessions(t.TempDir(), project)
	if result.ReadFailed != 1 || result.Success != 2 {
		t.Fatal("one bad compressed source stopped batch", result)
	}
	found := discoverSessions(storage, true)
	if len(found.Issues) != 1 || found.Interactive != 2 {
		t.Fatal("corrupt source was silently swallowed")
	}
}

func TestThreeSameIDVersionsRemainDistinct(t *testing.T) {
	storage, project := projectFixture(t)
	raw := project.Sessions[0]
	data, _ := os.ReadFile(raw.Path)
	project.Sessions = []sessionSummary{raw}
	for i := 0; i < 2; i++ {
		copy := raw
		copy.Path = filepath.Join(storage, "rollout-2026-10-13T09-00-00-version-"+string(rune('a'+i))+".jsonl.zst")
		version := bytes.Replace(data, []byte("synthetic final 中文"), []byte("different final "+string(rune('a'+i))), 1)
		writeSyntheticZstd(t, copy.Path, version)
		project.Sessions = append(project.Sessions, copy)
	}
	r := exportProjectSessions(t.TempDir(), project)
	if r.Success != 3 || r.Conflicts != 2 || r.failed() {
		t.Fatal("same ID lost distinct history", r)
	}
}

func TestCompressedPastedTextAndContextRegression(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	attachment := filepath.Join(makeDirectory(t, filepath.Join(home, "attachments", testAttachmentID)), "已粘贴的文本.txt")
	prompt := "# 完整中文 Prompt\n\n```xml\n<example>保留正文</example>\n```\n<!-- 用户注释 -->"
	if err := os.WriteFile(attachment, []byte(prompt), 0o600); err != nil {
		t.Fatal(err)
	}
	_, project := projectFixture(t)
	s := project.Sessions[0]
	raw, _ := os.ReadFile(s.Path)
	var transformed bytes.Buffer
	for _, line := range bytes.Split(raw, []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var record rolloutLine
		if err := json.Unmarshal(line, &record); err != nil {
			t.Fatal(err)
		}
		if record.Type == "response_item" {
			var item responseItem
			if err := json.Unmarshal(record.Payload, &item); err != nil {
				t.Fatal(err)
			}
			if item.Role == "user" {
				item.Content = []contentItem{{Type: "input_text", Text: "<environment_context><cwd>synthetic</cwd><shell>powershell</shell></environment_context>"}, {Type: "input_text", Text: "# Files pasted by the user:\n\n## \"synthetic preview\":\n" + attachment + "\n\n## My request:\n额外用户要求"}}
				record.Payload, _ = json.Marshal(item)
			}
		}
		if err := json.NewEncoder(&transformed).Encode(record); err != nil {
			t.Fatal(err)
		}
	}
	writeSyntheticZstd(t, s.Path+".zst", transformed.Bytes())
	s.Path += ".zst"
	markdown, count, err := renderSession(s, false)
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 || !strings.Contains(markdown, prompt) || !strings.Contains(markdown, "额外用户要求") || !strings.Contains(markdown, "# 💻 Assistant\n\nsynthetic final 中文") {
		t.Fatal("compressed shared pipeline lost real content")
	}
	for _, unwanted := range []string{"environment_context", "Files pasted by the user:", attachment, "synthetic progress"} {
		if strings.Contains(markdown, unwanted) {
			t.Fatal("context/attachment wrapper leaked", unwanted)
		}
	}
	if data, _ := os.ReadFile(attachment); string(data) != prompt {
		t.Fatal("attachment modified")
	}
}

func TestCompressedWithoutFinalPreservesUser(t *testing.T) {
	_, project := projectFixture(t)
	s := project.Sessions[0]
	data, _ := os.ReadFile(s.Path)
	data = bytes.ReplaceAll(data, []byte("final_answer"), []byte("commentary"))
	writeSyntheticZstd(t, s.Path+".zst", data)
	s.Path += ".zst"
	markdown, count, err := renderSession(s, false)
	if err != nil || count != 1 || !strings.Contains(markdown, "# 🧑 User\n\nsynthetic prompt 中文") || strings.Contains(markdown, "# 💻 Assistant") {
		t.Fatal("missing final removed prompt or fabricated reply", err)
	}
}
