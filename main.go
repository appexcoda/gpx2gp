package main

import (
	"archive/zip"
	_ "embed"
	"encoding/binary"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"
)

//go:embed score.gpss
var scoreGpss []byte

var verbose bool

func debug(format string, a ...interface{}) {
	if verbose {
		fmt.Printf("[DEBUG] "+format+"\n", a...)
	}
}

// BitReader implementation (MSB First)
type BitReader struct {
	data      []byte
	byteIdx   int
	bitOffset int
}

func NewBitReader(data []byte) *BitReader {
	return &BitReader{data: data, byteIdx: 0, bitOffset: 0}
}

func (br *BitReader) ReadBit() (byte, error) {
	if br.byteIdx >= len(br.data) {
		return 0, io.EOF
	}
	bit := (br.data[br.byteIdx] >> (7 - br.bitOffset)) & 1
	br.bitOffset++
	if br.bitOffset == 8 {
		br.bitOffset = 0
		br.byteIdx++
	}
	return bit, nil
}

func (br *BitReader) ReadBits(n int) (uint64, error) {
	var value uint64 = 0
	for i := 0; i < n; i++ {
		bit, err := br.ReadBit()
		if err != nil {
			return value, err
		}
		value = (value << 1) | uint64(bit)
	}
	return value, nil
}

func (br *BitReader) ReadBitsReversed(n int) (uint64, error) {
	var value uint64 = 0
	for i := 0; i < n; i++ {
		bit, err := br.ReadBit()
		if err != nil && err != io.EOF {
			return 0, err
		}
		if bit == 1 {
			value |= 1 << i
		}
	}
	return value, nil
}

func (br *BitReader) ReadByte() (byte, error) {
	val, err := br.ReadBits(8)
	return byte(val), err
}

func (br *BitReader) ReadBytes(n int) ([]byte, error) {
	buf := make([]byte, n)
	for i := 0; i < n; i++ {
		if br.bitOffset == 0 && br.byteIdx < len(br.data) {
			buf[i] = br.data[br.byteIdx]
			br.byteIdx++
		} else {
			b, err := br.ReadByte()
			if err != nil {
				return nil, err
			}
			buf[i] = b
		}
	}
	return buf, nil
}

func (br *BitReader) ReadAll() []byte {
	if br.byteIdx >= len(br.data) {
		return []byte{}
	}
	return br.data[br.byteIdx:]
}

// GpxFileSystem logic
type GpxFileSystem struct {
	Files []GpxFile
}

type GpxFile struct {
	FileName string
	FileSize int
	Data     []byte
}

func (fs *GpxFileSystem) Load(data []byte) error {
	reader := NewBitReader(data)
	return fs.readBlock(reader)
}

func (fs *GpxFileSystem) readBlock(src *BitReader) error {
	headerBytes, err := src.ReadBytes(4)
	if err != nil {
		return fmt.Errorf("failed to read header: %v", err)
	}
	header := string(headerBytes)
	debug("Container Header: %s", header)

	if header == "BCFZ" {
		decompressed, err := fs.decompress(src)
		if err != nil {
			return fmt.Errorf("decompression failed: %v", err)
		}
		debug("Decompression finished. Recovered %d bytes", len(decompressed))
		return fs.readUncompressedBlock(decompressed)
	} else if header == "BCFS" {
		return fs.readUncompressedBlock(src.ReadAll())
	} else {
		return fmt.Errorf("unsupported format header: %s", header)
	}
}

func (fs *GpxFileSystem) decompress(src *BitReader) ([]byte, error) {
	lenBytes, err := src.ReadBytes(4)
	if err != nil {
		return nil, err
	}
	expectedLength := int(binary.LittleEndian.Uint32(lenBytes))

	uncompressed := make([]byte, 0, expectedLength)

	for len(uncompressed) < expectedLength {
		flag, err := src.ReadBits(1)
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}

		if flag == 1 {
			// Compressed ref
			wordSize, err := src.ReadBits(4)
			if err == io.EOF {
				break
			}

			offset, err := src.ReadBitsReversed(int(wordSize))
			if err == io.EOF {
				break
			}

			size, err := src.ReadBitsReversed(int(wordSize))
			if err == io.EOF {
				break
			}

			sourcePosition := len(uncompressed) - int(offset)
			toRead := int(math.Min(float64(offset), float64(size)))

			if sourcePosition < 0 {
				for k := 0; k < toRead; k++ {
					uncompressed = append(uncompressed, 0)
				}
				continue
			}

			for i := 0; i < toRead; i++ {
				if sourcePosition+i < len(uncompressed) {
					uncompressed = append(uncompressed, uncompressed[sourcePosition+i])
				} else {
					uncompressed = append(uncompressed, 0)
				}
			}
		} else {
			// Literal
			size, err := src.ReadBitsReversed(2)
			if err == io.EOF {
				break
			}

			for i := 0; i < int(size); i++ {
				b, err := src.ReadByte()
				if err != nil {
					if err == io.EOF {
						break
					}
					return nil, err
				}
				uncompressed = append(uncompressed, b)
			}
		}
	}

	if len(uncompressed) > 4 {
		return uncompressed[4:], nil
	}
	return uncompressed, nil
}

func (fs *GpxFileSystem) readUncompressedBlock(data []byte) error {
	const sectorSize = 0x1000
	offset := sectorSize
	usedSectors := make(map[int]bool)

	getInt := func(pos int) int {
		if pos+4 > len(data) {
			return 0
		}
		return int(binary.LittleEndian.Uint32(data[pos : pos+4]))
	}

	getString := func(pos int, length int) string {
		if pos+length > len(data) {
			return ""
		}
		slice := data[pos : pos+length]
		end := 0
		for end < len(slice) {
			if slice[end] == 0 {
				break
			}
			end++
		}
		return string(slice[:end])
	}

	for offset+3 < len(data) {
		currentSectorIdx := offset / sectorSize
		if usedSectors[currentSectorIdx] {
			offset += sectorSize
			continue
		}

		entryType := getInt(offset)
		if entryType == 2 {
			fileName := getString(offset+0x04, 127)
			fileSize := getInt(offset + 0x8c)

			if fileName == "" || fileSize < 0 {
				offset += sectorSize
				continue
			}

			debug("Found File Header at Sector %d: %s (%d bytes)", currentSectorIdx, fileName, fileSize)

			file := GpxFile{
				FileName: fileName,
				FileSize: fileSize,
			}

			var fileData []byte
			dataPointerOffset := offset + 0x94
			sectorCount := 0

			for {
				sectorIndex := getInt(dataPointerOffset + 4*sectorCount)
				sectorCount++
				if sectorIndex == 0 {
					break
				}

				usedSectors[sectorIndex] = true
				sectorPos := sectorIndex * sectorSize
				if sectorPos >= len(data) {
					break
				}
				end := sectorPos + sectorSize
				if end > len(data) {
					end = len(data)
				}

				fileData = append(fileData, data[sectorPos:end]...)
			}

			if len(fileData) > fileSize {
				fileData = fileData[:fileSize]
			}
			file.Data = fileData
			fs.Files = append(fs.Files, file)
		}
		offset += sectorSize
	}
	return nil
}

// Zip archive creation logic
func createGpArchive(outputPath string, fs *GpxFileSystem) error {
	zipFile, err := os.Create(outputPath)
	if err != nil {
		return err
	}
	defer zipFile.Close()

	zw := zip.NewWriter(zipFile)
	defer zw.Close()

	writeEntry := func(name string, content []byte) error {
		f, err := zw.Create(name)
		if err != nil {
			return err
		}
		_, err = f.Write(content)
		return err
	}

	writeDir := func(name string) error {
		if !strings.HasSuffix(name, "/") {
			name = name + "/"
		}
		_, err := zw.Create(name)
		return err
	}

	// Static content
	if err := writeEntry("meta.json", []byte("{}")); err != nil {
		return err
	}
	if err := writeEntry("VERSION", []byte("7.0")); err != nil {
		return err
	}
	if err := writeEntry("Content/Preferences.json", []byte("{}")); err != nil {
		return err
	}

	// Write embedded score.gpss
	if err := writeEntry("Content/Stylesheets/score.gpss", scoreGpss); err != nil {
		return err
	}

	if err := writeDir("Content/ScoreViews"); err != nil {
		return err
	}

	// Dynamic content
	allowedFiles := map[string]bool{
		"score.gpif":          true,
		"PartConfiguration":   true,
		"LayoutConfiguration": true,
		"BinaryStylesheet":    true,
	}

	count := 0
	for _, file := range fs.Files {
		if allowedFiles[file.FileName] {
			targetPath := "Content/" + file.FileName
			if err := writeEntry(targetPath, file.Data); err != nil {
				return fmt.Errorf("failed to write %s: %v", file.FileName, err)
			}
			count++
		}
	}

	if count == 0 {
		return fmt.Errorf("no valid content files found in GPX")
	}

	return nil
}

func main() {
	var inputPath string
	var outputPath string

	flag.StringVar(&inputPath, "f", "", "Input GPX file")
	flag.StringVar(&inputPath, "file", "", "Input GPX file")
	flag.StringVar(&outputPath, "o", "", "Output filename")
	flag.StringVar(&outputPath, "out", "", "Output filename")
	flag.BoolVar(&verbose, "v", false, "Verbose output")

	flag.Parse()

	if inputPath == "" || outputPath == "" {
		fmt.Println("Usage: gpx2gp -f <input.gpx> -o <output_filename> [-v]")
		os.Exit(1)
	}

	// Ensure extension is .gp
	if !strings.HasSuffix(strings.ToLower(outputPath), ".gp") {
		outputPath += ".gp"
	}

	// Check for collision with input file
	absInput, _ := filepath.Abs(inputPath)
	absOutput, _ := filepath.Abs(outputPath)
	if absInput == absOutput {
		fmt.Println("Error: Output filename is the same as input filename.")
		os.Exit(1)
	}

	// Check if output file already exists
	if _, err := os.Stat(outputPath); err == nil {
		fmt.Printf("Error: Output file '%s' already exists.\n", outputPath)
		os.Exit(1)
	}

	start := time.Now()
	fmt.Printf("Reading: %s\n", inputPath)

	rawData, err := os.ReadFile(inputPath)
	if err != nil {
		fmt.Printf("Error reading file: %v\n", err)
		os.Exit(1)
	}

	fs := &GpxFileSystem{}
	if err := fs.Load(rawData); err != nil {
		fmt.Printf("Error processing GPX: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Found %d raw files. Writing archive to: %s\n", len(fs.Files), outputPath)

	if err := createGpArchive(outputPath, fs); err != nil {
		fmt.Printf("Error creating archive: %v\n", err)
		os.Remove(outputPath)
		os.Exit(1)
	}

	fmt.Printf("Success! Converted in %v.\n", time.Since(start))
}
