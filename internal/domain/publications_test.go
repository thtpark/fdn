package domain

import "testing"

func TestFileUploadLimitsAndSafeNames(t *testing.T) {
	valid := FileUploadRequest{Name: "evidence.txt", MediaType: "text/plain", Size: MaxFileSize, SHA256: Hash("fixture")}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*FileUploadRequest){
		func(r *FileUploadRequest) { r.Size = MaxFileSize + 1 },
		func(r *FileUploadRequest) { r.Size = -1 },
		func(r *FileUploadRequest) { r.Name = "../private" },
		func(r *FileUploadRequest) { r.Name = `C:\private` },
		func(r *FileUploadRequest) { r.Name = "." },
		func(r *FileUploadRequest) { r.Name = "line\nbreak" },
		func(r *FileUploadRequest) { r.MediaType = "text/plain; charset=utf-8" },
		func(r *FileUploadRequest) { r.SHA256 = "not-a-hash" },
		func(r *FileUploadRequest) { r.DeleteAfterDownload = true },
	} {
		request := valid
		change(&request)
		if err := request.Validate(); err == nil || AsFault(err).Code != Invalid {
			t.Fatalf("accepted invalid upload: %+v: %v", request, err)
		}
	}
	for _, mediaType := range []string{"image/svg+xml", "text/html", "application/javascript", "image/unknown"} {
		if InlineMedia(mediaType) {
			t.Fatal("unsafe or unsupported inline media", mediaType)
		}
	}
	if !InlineMedia("image/png") || !InlineMedia("video/mp4") {
		t.Fatal("supported images/videos are not inline-capable")
	}
}
