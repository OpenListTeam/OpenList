package buffer

import "io"

func GetSectionWriter(s Section) io.WriteSeeker {
	if sw, ok := s.(interface{ GetSectionWriter() SectionWriter }); ok {
		return sw.GetSectionWriter()
	}
	return io.NewOffsetWriter(s, 0)
}

func GetSectionReader(s Section) io.ReadSeeker {
	if sr, ok := s.(interface{ GetSectionReader() SectionReader }); ok {
		return sr.GetSectionReader()
	}
	return io.NewSectionReader(s, 0, s.Size())
}
