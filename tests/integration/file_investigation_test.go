//go:build integration

package integration_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/filestore"
	"github.com/thtpark/fdn/internal/foundation"
	"github.com/thtpark/fdn/internal/provider"
	"github.com/thtpark/fdn/internal/work"
)

type investigationFile struct {
	name      string
	mediaType string
	content   []byte
	question  string
	followup  string
	answer    string
	analyze   func([]byte) (string, error)
}

func investigationArchive(t *testing.T, parts map[string]string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	names := make([]string, 0, len(parts))
	for name := range parts {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(entry, parts[name]); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func inspectOfficeFixture(content []byte, names ...string) (string, error) {
	archive, err := zip.NewReader(bytes.NewReader(content), int64(len(content)))
	if err != nil {
		return "", err
	}
	var inspected []string
	for _, name := range names {
		part, err := archive.Open(name)
		if err != nil {
			return "", err
		}
		decoder := xml.NewDecoder(part)
		var text []string
		for {
			token, err := decoder.Token()
			if err == io.EOF {
				break
			}
			if err != nil {
				return "", errors.Join(err, part.Close())
			}
			if element, ok := token.(xml.StartElement); ok && element.Name.Local == "t" {
				var value string
				if err := decoder.DecodeElement(&value, &element); err != nil {
					return "", errors.Join(err, part.Close())
				}
				text = append(text, value)
			}
		}
		if err := part.Close(); err != nil {
			return "", err
		}
		if len(text) == 0 {
			return "", fmt.Errorf("no inspected text in Office fixture part %s", name)
		}
		inspected = append(inspected, name+": "+strings.Join(text, " | "))
	}
	return strings.Join(inspected, "; "), nil
}

func investigationFiles(t *testing.T) []investigationFile {
	t.Helper()
	word := investigationArchive(t, map[string]string{
		"[Content_Types].xml": `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/></Types>`,
		"_rels/.rels":         `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/></Relationships>`,
		"word/document.xml":   `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>Launch: 12 March</w:t></w:r></w:p><w:tbl><w:tr><w:tc><w:p><w:r><w:t>Budget</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>42 dollars</w:t></w:r></w:p></w:tc></w:tr></w:tbl><w:sectPr/></w:body></w:document>`,
	})
	slides := investigationArchive(t, map[string]string{
		"[Content_Types].xml":              `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/ppt/presentation.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.presentation.main+xml"/><Override PartName="/ppt/slides/slide1.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.slide+xml"/><Override PartName="/ppt/slides/slide2.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.slide+xml"/><Override PartName="/ppt/notesSlides/notesSlide1.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.notesSlide+xml"/></Types>`,
		"_rels/.rels":                      `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="ppt/presentation.xml"/></Relationships>`,
		"ppt/presentation.xml":             `<p:presentation xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><p:sldIdLst><p:sldId id="256" r:id="rId1"/><p:sldId id="257" r:id="rId2"/></p:sldIdLst><p:sldSz cx="9144000" cy="6858000"/><p:notesSz cx="6858000" cy="9144000"/></p:presentation>`,
		"ppt/_rels/presentation.xml.rels":  `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slide" Target="slides/slide1.xml"/><Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slide" Target="slides/slide2.xml"/></Relationships>`,
		"ppt/slides/slide1.xml":            `<p:sld xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main" xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"><p:cSld><p:spTree><p:sp><p:txBody><a:bodyPr/><a:lstStyle/><a:p><a:r><a:t>Revenue: 42 dollars</a:t></a:r></a:p></p:txBody></p:sp></p:spTree></p:cSld></p:sld>`,
		"ppt/slides/slide2.xml":            `<p:sld xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main" xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"><p:cSld><p:spTree><p:sp><p:txBody><a:bodyPr/><a:lstStyle/><a:p><a:r><a:t>Costs: 8 dollars</a:t></a:r></a:p></p:txBody></p:sp></p:spTree></p:cSld></p:sld>`,
		"ppt/slides/_rels/slide1.xml.rels": `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/notesSlide" Target="../notesSlides/notesSlide1.xml"/></Relationships>`,
		"ppt/notesSlides/notesSlide1.xml":  `<p:notes xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main" xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"><p:cSld><p:spTree><p:sp><p:txBody><a:bodyPr/><a:lstStyle/><a:p><a:r><a:t>Excludes tax</a:t></a:r></a:p></p:txBody></p:sp></p:spTree></p:cSld></p:notes>`,
	})
	photo := image.NewNRGBA(image.Rect(0, 0, 2, 1))
	photo.SetNRGBA(0, 0, color.NRGBA{R: 255, A: 255})
	photo.SetNRGBA(1, 0, color.NRGBA{B: 255, A: 255})
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, photo); err != nil {
		t.Fatal(err)
	}
	return []investigationFile{
		{name: "schedule.docx", mediaType: "application/vnd.openxmlformats-officedocument.wordprocessingml.document", content: word,
			question: "What are the launch date and the budget in the paragraph/table?", followup: "Which amount is in the budget table?",
			answer:  "word/document.xml: Launch: 12 March | Budget | 42 dollars",
			analyze: func(content []byte) (string, error) { return inspectOfficeFixture(content, "word/document.xml") }},
		{name: "overview.pptx", mediaType: "application/vnd.openxmlformats-officedocument.presentationml.presentation", content: slides,
			question: "What are the revenue, costs and slide 1 speaker-note caveat?", followup: "What caveat does slide 1's speaker note contain?",
			answer: "ppt/slides/slide1.xml: Revenue: 42 dollars; ppt/notesSlides/notesSlide1.xml: Excludes tax; ppt/slides/slide2.xml: Costs: 8 dollars",
			analyze: func(content []byte) (string, error) {
				return inspectOfficeFixture(content, "ppt/slides/slide1.xml", "ppt/notesSlides/notesSlide1.xml", "ppt/slides/slide2.xml")
			}},
		{name: "green-photo.png", mediaType: "image/png", content: encoded.Bytes(),
			question: "What colors appear in the photo's left and right image regions?", followup: "What is the right region's RGB value?",
			answer: "Photo 2x1: left region RGB(255,0,0); right region RGB(0,0,255).",
			analyze: func(content []byte) (string, error) {
				decoded, _, err := image.Decode(bytes.NewReader(content))
				if err != nil {
					return "", err
				}
				bounds := decoded.Bounds()
				left := color.NRGBAModel.Convert(decoded.At(bounds.Min.X, bounds.Min.Y)).(color.NRGBA)
				right := color.NRGBAModel.Convert(decoded.At(bounds.Max.X-1, bounds.Min.Y)).(color.NRGBA)
				return fmt.Sprintf("Photo %dx%d: left region RGB(%d,%d,%d); right region RGB(%d,%d,%d).",
					bounds.Dx(), bounds.Dy(), left.R, left.G, left.B, right.R, right.G, right.B), nil
			}},
	}
}

type investigationDownloads struct {
	base   http.RoundTripper
	chunks atomic.Int64
}

func (transport *investigationDownloads) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.Method == http.MethodGet && strings.HasSuffix(request.URL.Path, "/chunks") {
		transport.chunks.Add(1)
	}
	return transport.base.RoundTrip(request)
}

func TestOfficeAndPhotoQuestionsUseActualPrivateBytes(t *testing.T) {
	for _, preferred := range []string{"codex", "copilot"} {
		for _, fixture := range investigationFiles(t) {
			for _, requester := range []string{"founder", "delegate"} {
				t.Run(preferred+"/"+fixture.name+"/"+requester, func(t *testing.T) {
					h := newHarnessWithFiles(t, foundation.FileStoreOptions{Permissions: filestore.PermissionFunc(
						func(context.Context, string, []uint32) error { return nil })})
					ctx := context.Background()
					sender, stranger := h.founder, h.other
					origin := "human"
					if requester == "delegate" {
						project, err := api.Call[domain.Project](ctx, h.founder, "POST", "/v1/projects", domain.ProjectRequest{Command: command(), Name: "Investigation"})
						if err != nil {
							t.Fatal(err)
						}
						if _, err := api.Call[domain.Project](ctx, h.founder, "POST", api.Path("projects", project.ID, "delegates"),
							domain.GrantRequest{Command: domain.Command{OperationID: domain.NewID("grant"), ExpectedRevision: project.Revision}, AgentID: h.otherAgent.ID}); err != nil {
							t.Fatal(err)
						}
						sender, stranger = h.other, h.founder
						origin = "ai"
					}
					x := &fileRequestExecutor{t: t, preferred: preferred, content: fixture.content, analyze: fixture.analyze}
					engine := newEngine(t, h, &x.base)
					engine.Executor, x.engine = x, engine
					engine.Config.Provider, engine.Config.Model = preferred, "fixture-model"
					skipHealthOnboarding(t, engine)
					file, err := sender.UploadFile(ctx, domain.FileUploadRequest{Command: command(), Name: fixture.name, MediaType: fixture.mediaType,
						Size: int64(len(fixture.content)), SHA256: domain.Hash(string(fixture.content))}, bytes.NewReader(fixture.content))
					if err != nil {
						t.Fatal(err)
					}
					x.file = file
					transport := &investigationDownloads{base: h.worker.HTTP.Transport}
					h.worker.HTTP.Transport = transport
					t.Cleanup(func() { h.worker.HTTP.Transport = transport.base })
					source, err := api.Call[domain.Message](ctx, sender, "POST", "/v1/messages", map[string]any{
						"operation_id": command().OperationID, "recipient_id": h.workerAgent.ID, "kind": "direct",
						"origin": origin, "body": fixture.question, "attachment_ids": []string{file.ID},
					})
					if err != nil {
						t.Fatal(err)
					}
					for turn := 0; turn < 2; turn++ {
						if _, err := engine.Step(ctx, false); err != nil {
							t.Fatal(err)
						}
						if x.executions != turn {
							t.Fatal("file content was inspected during text planning", x.executions)
						}
						engine = restartAuthenticationEngine(t, engine)
						x.engine = engine
						if _, err := engine.Step(ctx, false); err != nil {
							t.Fatal(err)
						}
						thread, err := api.Call[[]domain.Message](ctx, sender, "GET", api.Path("messages", source.ID, "thread"), nil)
						if err != nil {
							t.Fatal(err)
						}
						var result domain.Message
						for _, message := range thread {
							if message.ReplyToID == source.ID {
								result = message
							}
						}
						if result.ID == "" || result.RecipientID != sender.Credential.AgentID ||
							!strings.Contains(result.Body, fixture.answer) || result.ResponseRequest {
							t.Fatal("actual document/image answer or exact requesting thread was lost", result)
						}
						if transport.chunks.Load() != 1 {
							t.Fatal("follow-up redownloaded an already verified copy", transport.chunks.Load())
						}
						if turn == 0 {
							source, err = api.Call[domain.Message](ctx, sender, "POST", "/v1/messages", map[string]any{
								"operation_id": command().OperationID, "recipient_id": h.workerAgent.ID, "kind": "direct",
								"origin": origin, "body": fixture.followup, "reply_to_id": result.ID,
							})
							if err != nil || len(source.Attachments) != 0 {
								t.Fatal("follow-up should reference the original file through its thread", source, err)
							}
						}
					}
					if x.planning != 2 || x.executions != 2 {
						t.Fatal("independent file questions were skipped or repeated", x.planning, x.executions)
					}
					if _, err := api.Call[domain.SharedFile](ctx, stranger, "GET", api.Path("files", file.ID), nil); err == nil || domain.AsFault(err).Code != domain.Denied {
						t.Fatal("analysis broadened private readership, including Founder", err)
					}
					for range 2 {
						if _, err := engine.Step(ctx, false); err != nil {
							t.Fatal(err)
						}
					}
					if x.executions != 2 {
						t.Fatal("completed file investigation repeated on polling")
					}
				})
			}
		}
	}
}

func TestApprovedTaskFileInvestigationUsesPublishedAttachments(t *testing.T) {
	for _, preferred := range []string{"codex", "copilot"} {
		for _, fixture := range investigationFiles(t) {
			t.Run(preferred+"/"+fixture.name, func(t *testing.T) {
				h := newHarnessWithFiles(t, foundation.FileStoreOptions{Permissions: filestore.PermissionFunc(
					func(context.Context, string, []uint32) error { return nil })})
				ctx := context.Background()
				x := &fileRequestExecutor{t: t, preferred: preferred, content: fixture.content, analyze: fixture.analyze}
				engine := newEngine(t, h, &x.base)
				engine.Executor, x.engine = x, engine
				engine.Config.Provider, engine.Config.Model = preferred, "fixture-model"
				skipHealthOnboarding(t, engine)
				task := readyForEngine(t, h, engine)
				task = h.change(t, h.founder, task.ID, "provider", func(change *domain.TaskChange) {
					change.Provider, change.Model = preferred, "fixture-model"
				})
				file, err := h.founder.UploadFile(ctx, domain.FileUploadRequest{Command: command(), Name: fixture.name, MediaType: fixture.mediaType,
					Size: int64(len(fixture.content)), SHA256: domain.Hash(string(fixture.content))}, bytes.NewReader(fixture.content))
				if err != nil {
					t.Fatal(err)
				}
				x.file, err = api.Call[domain.SharedFile](ctx, h.founder, "POST", api.Path("files", file.ID, "publish"), map[string]any{
					"operation_id": command().OperationID, "expected_revision": file.Revision, "reason": "Publish this test input for the shared task",
				})
				if err != nil {
					t.Fatal(err)
				}
				task = h.change(t, h.founder, task.ID, "edit", func(change *domain.TaskChange) {
					description, criteria := fixture.question, "Answer using actual inspected content with source locations."
					change.Description, change.AcceptanceCriteria = &description, &criteria
					ids := []string{file.ID}
					change.AttachmentIDs = &ids
				})
				task = h.change(t, h.founder, task.ID, "approve", func(change *domain.TaskChange) { change.SpecGeneration = task.SpecGeneration })
				for range 2 {
					if _, err := engine.Step(ctx, false); err != nil {
						t.Fatal(err)
					}
				}
				current, err := api.Call[domain.Task](ctx, h.worker, "GET", api.Path("tasks", task.ID), nil)
				if err != nil || current.Status != domain.Completed || current.Result == nil || !strings.Contains(current.Result.Summary, fixture.answer) {
					t.Fatal("ordinary claimed task did not report the actual file investigation", current, err)
				}
				if x.planning != 0 || x.executions != 1 {
					t.Fatal("approved task was turned into a conversation or repeated", x.planning, x.executions)
				}
			})
		}
	}
}

type unavailableBrowserExecutor struct {
	base       executor
	t          *testing.T
	planning   int
	executions int
	workspace  string
}

func (x *unavailableBrowserExecutor) Check(ctx context.Context, name string) (provider.Health, error) {
	return x.base.Check(ctx, name)
}

func (x *unavailableBrowserExecutor) Invoke(ctx context.Context, invocation provider.Invocation) (provider.Reply, error) {
	_, raw, found := strings.Cut(invocation.Prompt, "\nFDN_CONTEXT_JSON\n")
	if !found {
		x.t.Fatal("browser request omitted its source context")
	}
	var launch work.Launch
	if err := json.Unmarshal([]byte(raw), &launch); err != nil {
		return provider.Reply{}, err
	}
	if invocation.Started != nil {
		if err := invocation.Started(123456); err != nil {
			return provider.Reply{}, err
		}
	}
	if !invocation.Coding {
		x.planning++
		if launch.Phase != "message" || launch.Message == nil {
			x.t.Fatal("browser request bypassed tool-disabled message planning", launch)
		}
		return provider.Reply{Outcome: "responded", Summary: "The requested browser download and inspection need tools.",
			Action: &provider.ConversationAction{Instructions: launch.Message.Body}}, nil
	}
	x.executions++
	x.workspace = invocation.Workspace
	if launch.Phase != domain.ExecutionRun || launch.ConversationID == "" ||
		!strings.Contains(launch.ActionInstructions, "web browser") || !strings.Contains(launch.ActionInstructions, "https://example.org/overview.pptx") {
		x.t.Fatal("browser method or exact source was lost before claimed execution", launch)
	}
	return provider.Reply{Outcome: "paused", Summary: "Cannot start the requested download: no local browser automation tool is available.",
		Questions: []provider.Question{{Body: "Please arrange an available browser tool, or explicitly permit a direct HTTP download."}}}, nil
}

func TestBrowserInvestigationReportsUnavailableToolWithoutRetry(t *testing.T) {
	for _, preferred := range []string{"codex", "copilot"} {
		t.Run(preferred, func(t *testing.T) {
			h := newHarness(t)
			ctx := context.Background()
			x := &unavailableBrowserExecutor{t: t}
			engine := newEngine(t, h, &x.base)
			engine.Executor = x
			engine.Config.Provider, engine.Config.Model = preferred, "fixture-model"
			skipHealthOnboarding(t, engine)
			source, err := api.Call[domain.Message](ctx, h.founder, "POST", "/v1/messages", map[string]any{
				"operation_id": command().OperationID, "recipient_id": h.workerAgent.ID, "kind": "direct",
				"origin": "human", "body": "Use a web browser to download https://example.org/overview.pptx and explain slide 1.",
			})
			if err != nil {
				t.Fatal(err)
			}
			for range 2 {
				if _, err := engine.Step(ctx, false); err != nil {
					t.Fatal(err)
				}
			}
			thread, err := api.Call[[]domain.Message](ctx, h.founder, "GET", api.Path("messages", source.ID, "thread"), nil)
			if err != nil || len(thread) != 2 {
				t.Fatal("missing browser did not produce a private result", thread, err)
			}
			var result domain.Message
			for _, message := range thread {
				if message.ReplyToID == source.ID {
					result = message
				}
			}
			if !strings.Contains(result.Body, "Action paused") || !strings.Contains(result.Body, "no local browser automation tool") ||
				!strings.Contains(result.Body, "Please arrange") || !result.ResponseRequest {
				t.Fatal("missing browser was guessed, hidden or reported as completion", result)
			}
			files, err := os.ReadDir(x.workspace)
			if err != nil || len(files) != 0 {
				t.Fatal("missing browser produced fake download/analysis artifacts", files, err)
			}
			tasks, err := api.Call[domain.Page[domain.Task]](ctx, h.worker, "GET", "/v1/tasks?project="+domain.AdhocProjectID, nil)
			if err != nil || len(tasks.Items) != 1 || tasks.Items[0].Status != domain.Paused {
				t.Fatal("blocked browser action was treated as completed work", tasks, err)
			}
			engine = restartAuthenticationEngine(t, engine)
			for range 2 {
				if _, err := engine.Step(ctx, false); err != nil {
					t.Fatal(err)
				}
			}
			if x.planning != 1 || x.executions != 1 {
				t.Fatal("a blocked browser request was retried after restart", x.planning, x.executions)
			}
		})
	}
}
