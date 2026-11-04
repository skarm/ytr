package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/skarm/ytr/internal/apidoc"
)

func TestGeneratedFixtureCompilesAndExecutesIndependentExpectations(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	fixture := t.TempDir()
	if err := os.Mkdir(filepath.Join(fixture, "typed"), 0o750); err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob(filepath.Join(root, "typed", "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if strings.HasSuffix(file, "_gen.go") || strings.HasSuffix(file, "_test.go") {
			continue
		}
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(fixture, "typed", filepath.Base(file)), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	module := "module example.com/generated\n\ngo 1.27.0\n\nrequire github.com/skarm/ytr v0.0.0\nreplace github.com/skarm/ytr => " + strconv.Quote(root) + "\n"
	if err := os.WriteFile(filepath.Join(fixture, "go.mod"), []byte(module), 0o600); err != nil {
		t.Fatal(err)
	}
	models := compiledFixtureModels()
	catalog, err := apidoc.Load(filepath.Join(root, "api/catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	var subset []apidoc.Operation
	for _, binding := range models.Operations {
		for _, operation := range catalog.Operations {
			if operation.Name == binding.Name {
				subset = append(subset, operation)
			}
		}
	}
	catalog.Operations = subset
	models.Revision = catalog.Revision
	if err := apidoc.ValidateModels(models, catalog); err != nil {
		t.Fatal(err)
	}
	for _, output := range []struct{ name, source string }{{"models_gen.go", modelTemplate}, {"client_gen.go", clientTemplate}} {
		data, err := renderTypedFile(output.name, output.source, models)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(fixture, "typed", output.name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(fixture, "typed", "fixture_test.go"), []byte(compiledFixtureTest), 0o600); err != nil {
		t.Fatal(err)
	}
	run := func() ([]byte, error) {
		cmd := exec.CommandContext(t.Context(), "go", "test", "-count=1", "./typed")
		cmd.Dir = fixture
		cmd.Env = append(os.Environ(), "GOWORK=off", "GOPROXY=off")
		return cmd.CombinedOutput()
	}
	if output, err := run(); err != nil {
		t.Fatalf("generated fixture: %v\n%s", err, output)
	}
	// This realistic template mutation still compiles. The independent HTTP
	// expectation must reject dropping one value of a repeated query parameter.
	mutated := strings.ReplaceAll(clientTemplate, "query.Add(", "query.Set(")
	data, err := renderTypedFile("client_gen.go", mutated, models)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture, "typed", "client_gen.go"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if output, err := run(); err == nil || !strings.Contains(string(output), "repeated query lost") {
		t.Fatalf("mutation escaped semantic check: %v\n%s", err, output)
	}
}

func compiledFixtureModels() apidoc.Models {
	models := apidoc.Models{Version: 1, Types: []apidoc.ModelType{
		{Name: "GetEntityRequest", Kind: "request", Fields: []apidoc.ModelField{{Name: "EntityType", Type: "ytr.EntityType"}, {Name: "EntityID", Type: "string"}, {Name: "Force", Type: "*bool"}, {Name: "Count", Type: "int64"}, {Name: "Fields", Type: "[]string"}, {Name: "From", Type: "*string"}, {Name: "To", Type: "*string"}, {Name: "Version", Type: "*string"}}},
		{Name: "CreateIssueRequest", Kind: "request", Fields: []apidoc.ModelField{{Name: "Body", Type: "Payload"}, {Name: "ExtraFields", Type: "ytr.Fields"}}},
		{Name: "UploadTemporaryAttachmentRequest", Kind: "request", Fields: []apidoc.ModelField{{Name: "File", Type: "ytr.File"}}},
		{Name: "DownloadIssueAttachmentRequest", Kind: "request", Fields: []apidoc.ModelField{{Name: "IssueID", Type: "string"}, {Name: "FileID", Type: "string"}, {Name: "Filename", Type: "string"}}},
		{Name: "Payload", Kind: "struct", Open: true, Fields: []apidoc.ModelField{{Name: "Flag", JSON: "flag", Type: "Optional[bool]", Optional: true}, {Name: "Choice", JSON: "choice", Type: "Optional[Choice]", Optional: true}, {Name: "Event", JSON: "event", Type: "Optional[Event]", Optional: true}}},
		{Name: "Choice", Kind: "union", Variants: []apidoc.ModelVariant{{Name: "Text", Type: "string", JSONKind: "string"}, {Name: "Number", Type: "json.Number", JSONKind: "number"}}},
		{Name: "Event", Kind: "union", Discriminator: "type", Variants: []apidoc.ModelVariant{{Name: "Created", Type: "Created", JSONKind: "object", Tags: []string{"created"}}, {Name: "Deleted", Type: "Deleted", JSONKind: "object", Tags: []string{"deleted"}}}},
		{Name: "Created", Kind: "struct", Fields: []apidoc.ModelField{{Name: "Type", JSON: "type", Type: "string"}, {Name: "Label", JSON: "label", Type: "string"}}},
		{Name: "Deleted", Kind: "struct", Fields: []apidoc.ModelField{{Name: "Type", JSON: "type", Type: "string"}, {Name: "Count", JSON: "count", Type: "int64"}}},
	}}
	models.Operations = []apidoc.ModelOperation{
		{Name: "GetEntity", Request: "GetEntityRequest", Response: "Payload", Encoding: "none", Source: "api/entities/get-entity.md", Paths: []apidoc.ModelPath{{Name: "EntityType", Type: "ytr.EntityType"}, {Name: "EntityID", Type: "string"}}, Queries: []apidoc.ModelQuery{{Field: "Force", Key: "force", Type: "bool", Optional: true}, {Field: "Count", Key: "count", Type: "int64"}, {Field: "Fields", Key: "fields", Type: "[]string", CSV: true}, {Field: "From", Key: "createdAt", Type: "string", Optional: true, Prefix: "from:"}, {Field: "To", Key: "createdAt", Type: "string", Optional: true, Prefix: "to:"}}, Headers: []apidoc.ModelHeader{{Field: "Version", Key: "If-Match"}}},
		{Name: "CreateIssue", Request: "CreateIssueRequest", Response: "Payload", Encoding: "json", Body: "Payload", Extras: true, Source: "api/issues/create-issue.md"},
		{Name: "UploadTemporaryAttachment", Request: "UploadTemporaryAttachmentRequest", Response: "Payload", Encoding: "multipart", Source: "api/issues/temp-attachment.md"},
		{Name: "DownloadIssueAttachment", Request: "DownloadIssueAttachmentRequest", Stream: true, Encoding: "none", Source: "api/issues/get-attachment.md", Paths: []apidoc.ModelPath{{Name: "IssueID", Type: "string"}, {Name: "FileID", Type: "string"}, {Name: "Filename", Type: "string"}}},
	}
	for _, name := range []string{"Reference", "IDInput", "StringOrNumber"} {
		models.Types = append(models.Types, apidoc.ModelType{Name: name, Kind: "union", Variants: []apidoc.ModelVariant{{Name: "String", Type: "string", JSONKind: "string"}, {Name: "Number", Type: "json.Number", JSONKind: "number"}}})
	}
	models.Types = append(models.Types, apidoc.ModelType{Name: "Évidence", Kind: "struct", Open: true, Fields: []apidoc.ModelField{{Name: "Значение", JSON: "ключ", Type: "Optional[bool]", Optional: true}}})
	return models
}

const compiledFixtureTest = `package typed_test
import("context";"encoding/json";"io";"net/http";"reflect";"strings";"testing";"github.com/skarm/ytr";"example.com/generated/typed")
type wire func(*http.Request)(*http.Response,error)
func(f wire)RoundTrip(r *http.Request)(*http.Response,error){return f(r)}
func TestGeneratedWire(t *testing.T){
 calls:=0
 client,err:=typed.New(ytr.Config{Organization:ytr.Organization{Kind:ytr.Organization360,ID:"fixture"},TokenSource:ytr.OAuthToken("fixture-token"),HTTPClient:&http.Client{Transport:wire(func(r *http.Request)(*http.Response,error){
  calls++
  switch calls {
  case 1:
   if r.URL.EscapedPath()!="/v3/entities/goal/A%2FB"||r.URL.Query().Get("force")!="false"||r.URL.Query().Get("count")!="0"||r.URL.Query().Get("fields")!="key,summary"||r.Header.Get("If-Match")!="7"{t.Error("query/header/path changed",r.URL,r.Header)}
   if !reflect.DeepEqual(r.URL.Query()["createdAt"],[]string{"from:1","to:2"}){t.Error("repeated query lost",r.URL.Query())}
  case 2:
   data,_:=io.ReadAll(r.Body);r.Body.Close();var fields map[string]json.RawMessage;if json.Unmarshal(data,&fields)!=nil||string(fields["flag"])!="false"||string(fields["choice"])!="9007199254740993"||string(fields["extra"])!="null"||string(fields["event"])!="{\"type\":\"created\",\"label\":\"x\"}"{t.Error("request JSON changed",string(data))}
  case 3:
   reader,err:=r.MultipartReader();if err!=nil{t.Fatal(err)};part,err:=reader.NextPart();if err!=nil{t.Fatal(err)};data,_:=io.ReadAll(part);r.Body.Close();if part.FileName()!="x.bin"||string(data)!="payload"{t.Error("multipart changed")}
  }
  return &http.Response{StatusCode:200,Header:make(http.Header),Body:io.NopCloser(strings.NewReader("{\"flag\":false,\"choice\":9007199254740993,\"custom\":0}"))},nil
 })}});if err!=nil{t.Fatal(err)}
 result,err:=client.GetEntity(context.Background(),typed.GetEntityRequest{EntityType:ytr.Goal,EntityID:"A/B",Force:typed.Ptr(false),Fields:[]string{"key","summary"},From:typed.Ptr("1"),To:typed.Ptr("2"),Version:typed.Ptr("7")});if err!=nil{t.Fatal(err)}
 flag,present:=result.Data.Flag.Get();choice,ok:=result.Data.Choice.Get();if flag||!present||!ok||choice.Number==nil||*choice.Number!="9007199254740993"||string(result.Data.AdditionalFields["custom"])!="0"{t.Fatal("response decoding changed",result)}
 _,err=client.CreateIssue(context.Background(),typed.CreateIssueRequest{Body:typed.Payload{Flag:typed.Value(false),Choice:typed.Value(typed.Choice{Number:typed.Ptr(json.Number("9007199254740993"))}),Event:typed.Value(typed.Event{Created:&typed.Created{Type:"created",Label:"x"}})},ExtraFields:ytr.Fields{"extra":nil}});if err!=nil{t.Fatal(err)}
 _,err=client.UploadTemporaryAttachment(context.Background(),typed.UploadTemporaryAttachmentRequest{File:ytr.File{Name:"x.bin",Reader:strings.NewReader("payload")}});if err!=nil{t.Fatal(err)}
 download,err:=client.DownloadIssueAttachment(context.Background(),typed.DownloadIssueAttachmentRequest{IssueID:"A-1",FileID:"1",Filename:"x.bin"});if err!=nil{t.Fatal(err)};download.Body.Close()
 _,err=json.Marshal(typed.Event{Created:&typed.Created{Type:"deleted"}});if err==nil{t.Fatal("invalid discriminator accepted")}
 var unicodeValue typed.Évidence
 if err:=json.Unmarshal([]byte("{\"ключ\":false,\"custom\":9007199254740993}"),&unicodeValue);err!=nil{t.Fatal(err)}
 if value,present:=unicodeValue.Значение.Get();value||!present||string(unicodeValue.AdditionalFields["custom"])!="9007199254740993"{t.Fatal("Unicode model/JSON names changed")}
 if calls!=4{t.Fatal("wrong number of calls",calls)}
}
`
