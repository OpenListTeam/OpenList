package torrent

import (
	"bytes"
	"fmt"
	"io"
	"sort"
	"strconv"
)

// bencode 编码

// BencodeEncode 将值编码为 bencode 格式
func BencodeEncode(v interface{}) ([]byte, error) {
	var buf bytes.Buffer
	if err := bencodeEncodeValue(&buf, v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func bencodeEncodeValue(w io.Writer, v interface{}) error {
	switch val := v.(type) {
	case int:
		return bencodeEncodeInt(w, int64(val))
	case int64:
		return bencodeEncodeInt(w, val)
	case string:
		return bencodeEncodeString(w, val)
	case []byte:
		return bencodeEncodeBytes(w, val)
	case []interface{}:
		return bencodeEncodeList(w, val)
	case map[string]interface{}:
		return bencodeEncodeDict(w, val)
	case OrderedDict:
		return bencodeEncodeOrderedDict(w, val)
	default:
		return fmt.Errorf("bencode: unsupported type %T", v)
	}
}

func bencodeEncodeInt(w io.Writer, v int64) error {
	_, err := fmt.Fprintf(w, "i%de", v)
	return err
}

func bencodeEncodeString(w io.Writer, v string) error {
	_, err := fmt.Fprintf(w, "%d:%s", len(v), v)
	return err
}

func bencodeEncodeBytes(w io.Writer, v []byte) error {
	_, err := fmt.Fprintf(w, "%d:", len(v))
	if err != nil {
		return err
	}
	_, err = w.Write(v)
	return err
}

func bencodeEncodeList(w io.Writer, v []interface{}) error {
	if _, err := w.Write([]byte("l")); err != nil {
		return err
	}
	for _, item := range v {
		if err := bencodeEncodeValue(w, item); err != nil {
			return err
		}
	}
	_, err := w.Write([]byte("e"))
	return err
}

func bencodeEncodeDict(w io.Writer, v map[string]interface{}) error {
	// bencode 字典要求 key 按字典序排列
	keys := make([]string, 0, len(v))
	for k := range v {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	if _, err := w.Write([]byte("d")); err != nil {
		return err
	}
	for _, k := range keys {
		if err := bencodeEncodeString(w, k); err != nil {
			return err
		}
		if err := bencodeEncodeValue(w, v[k]); err != nil {
			return err
		}
	}
	_, err := w.Write([]byte("e"))
	return err
}

// OrderedDict 有序字典，保持插入顺序
type OrderedDict struct {
	Keys   []string
	Values map[string]interface{}
}

func NewOrderedDict() OrderedDict {
	return OrderedDict{
		Keys:   make([]string, 0),
		Values: make(map[string]interface{}),
	}
}

func (d *OrderedDict) Set(key string, value interface{}) {
	if _, exists := d.Values[key]; !exists {
		d.Keys = append(d.Keys, key)
	}
	d.Values[key] = value
}

func (d *OrderedDict) Get(key string) (interface{}, bool) {
	v, ok := d.Values[key]
	return v, ok
}

func bencodeEncodeOrderedDict(w io.Writer, d OrderedDict) error {
	// 按字典序排列 key（bencode 规范要求）
	keys := make([]string, len(d.Keys))
	copy(keys, d.Keys)
	sort.Strings(keys)

	if _, err := w.Write([]byte("d")); err != nil {
		return err
	}
	for _, k := range keys {
		if err := bencodeEncodeString(w, k); err != nil {
			return err
		}
		if err := bencodeEncodeValue(w, d.Values[k]); err != nil {
			return err
		}
	}
	_, err := w.Write([]byte("e"))
	return err
}

// bencode 解码

// BencodeDecode 从字节数组解码 bencode 数据
func BencodeDecode(data []byte) (interface{}, error) {
	reader := bytes.NewReader(data)
	val, err := bencodeDecodeValue(reader)
	if err != nil {
		return nil, err
	}
	return val, nil
}

func bencodeDecodeValue(r *bytes.Reader) (interface{}, error) {
	b, err := r.ReadByte()
	if err != nil {
		return nil, err
	}

	switch {
	case b == 'i':
		return bencodeDecodeInt(r)
	case b == 'l':
		return bencodeDecodeList(r)
	case b == 'd':
		return bencodeDecodeDict(r)
	case b >= '0' && b <= '9':
		r.UnreadByte()
		return bencodeDecodeString(r)
	default:
		return nil, fmt.Errorf("bencode: unexpected byte '%c' at position %d", b, int64(r.Len()))
	}
}

func bencodeDecodeInt(r *bytes.Reader) (int64, error) {
	var buf bytes.Buffer
	for {
		b, err := r.ReadByte()
		if err != nil {
			return 0, err
		}
		if b == 'e' {
			break
		}
		buf.WriteByte(b)
	}
	return strconv.ParseInt(buf.String(), 10, 64)
}

func bencodeDecodeString(r *bytes.Reader) ([]byte, error) {
	// 读取长度
	var lenBuf bytes.Buffer
	for {
		b, err := r.ReadByte()
		if err != nil {
			return nil, err
		}
		if b == ':' {
			break
		}
		lenBuf.WriteByte(b)
	}
	length, err := strconv.ParseInt(lenBuf.String(), 10, 64)
	if err != nil {
		return nil, fmt.Errorf("bencode: invalid string length: %v", err)
	}
	if length < 0 || length > 100*1024*1024 {
		return nil, fmt.Errorf("bencode: string length out of bounds: %d", length)
	}
	// Safe to convert to int: bounds check above ensures length <= 100MB which fits in int32
	data := make([]byte, int(length))
	_, err = io.ReadFull(r, data)
	if err != nil {
		return nil, err
	}
	return data, nil
}

func bencodeDecodeList(r *bytes.Reader) ([]interface{}, error) {
	var list []interface{}
	for {
		b, err := r.ReadByte()
		if err != nil {
			return nil, err
		}
		if b == 'e' {
			return list, nil
		}
		r.UnreadByte()
		val, err := bencodeDecodeValue(r)
		if err != nil {
			return nil, err
		}
		list = append(list, val)
	}
}

func bencodeDecodeDict(r *bytes.Reader) (map[string]interface{}, error) {
	dict := make(map[string]interface{})
	for {
		b, err := r.ReadByte()
		if err != nil {
			return nil, err
		}
		if b == 'e' {
			return dict, nil
		}
		r.UnreadByte()
		keyBytes, err := bencodeDecodeString(r)
		if err != nil {
			return nil, err
		}
		val, err := bencodeDecodeValue(r)
		if err != nil {
			return nil, err
		}
		dict[string(keyBytes)] = val
	}
}
