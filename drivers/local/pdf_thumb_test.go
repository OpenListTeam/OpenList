package local

import (
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
)

func TestSupportsThumbnail(t *testing.T) {
	oldImages := conf.SlicesMap[conf.ImageTypes]
	oldVideos := conf.SlicesMap[conf.VideoTypes]
	conf.SlicesMap[conf.ImageTypes] = []string{"jpg"}
	conf.SlicesMap[conf.VideoTypes] = []string{"mp4"}
	t.Cleanup(func() {
		conf.SlicesMap[conf.ImageTypes] = oldImages
		conf.SlicesMap[conf.VideoTypes] = oldVideos
	})

	tests := []struct {
		name         string
		fileName     string
		pdfThumbnail bool
		want         bool
	}{
		{name: "image", fileName: "cover.jpg", want: true},
		{name: "video", fileName: "movie.mp4", want: true},
		{name: "PDF disabled by default", fileName: "document.pdf", want: false},
		{name: "unrelated document", fileName: "document.txt", pdfThumbnail: true, want: false},
		{name: "PDF enabled when renderer is available", fileName: "document.PDF", pdfThumbnail: true, want: pdfThumbnailSupported()},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := &Local{Addition: Addition{PDFThumbnail: tt.pdfThumbnail}}
			if got := d.supportsThumbnail(tt.fileName); got != tt.want {
				t.Fatalf("supportsThumbnail(%q) = %v, want %v", tt.fileName, got, tt.want)
			}
		})
	}
}
