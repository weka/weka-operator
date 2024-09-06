package util

import (
	"bytes"
	"compress/zlib"
	"fmt"
)

func CompressBytes(data []byte) ([]byte, error) {
	var compressedData bytes.Buffer
	writer := zlib.NewWriter(&compressedData)

	_, err := writer.Write(data)
	if err != nil {
		err = fmt.Errorf("failed to write data to zlib writer: %w", err)
		return nil, err
	}

	err = writer.Close()
	if err != nil {
		err = fmt.Errorf("failed to close zlib writer: %w", err)
		return nil, err
	}

	return compressedData.Bytes(), nil
}

func DecompressBytes(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("no data to decompress")
	}

	reader, err := zlib.NewReader(bytes.NewReader(data))
	if err != nil {
		err = fmt.Errorf("failed to create zlib reader: %w", err)
		return nil, err
	}
	defer reader.Close()

	var decompressedData bytes.Buffer
	_, err = decompressedData.ReadFrom(reader)
	if err != nil {
		err = fmt.Errorf("failed to read data from zlib reader: %w", err)
		return nil, err
	}

	return decompressedData.Bytes(), nil
}
