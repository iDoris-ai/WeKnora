package session

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/models/asr"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func encodedAttachment(data string, declaredSize int64) AttachmentUpload {
	return AttachmentUpload{
		Data:     base64.StdEncoding.EncodeToString([]byte(data)),
		FileName: "file.txt",
		FileSize: declaredSize,
	}
}

func TestDecodeAndValidateAttachmentUploadsUsesActualBytes(t *testing.T) {
	uploads := []AttachmentUpload{encodedAttachment("123456", 1)}

	_, err := decodeAndValidateAttachmentUploads(uploads, 5, 5, 100)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds size limit")
}

func TestDecodeAndValidateAttachmentUploadsBoundsCountAndTotal(t *testing.T) {
	t.Run("count", func(t *testing.T) {
		uploads := make([]AttachmentUpload, 6)
		for i := range uploads {
			uploads[i] = encodedAttachment("x", 1)
		}
		_, err := decodeAndValidateAttachmentUploads(uploads, 5, 10, 100)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "at most 5")
	})

	t.Run("total", func(t *testing.T) {
		uploads := []AttachmentUpload{
			encodedAttachment(strings.Repeat("a", 6), 6),
			encodedAttachment(strings.Repeat("b", 6), 6),
		}
		_, err := decodeAndValidateAttachmentUploads(uploads, 5, 10, 10)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "total request limit")
	})
}

func TestDecodeAndValidateAttachmentUploadsReturnsDecodedBytes(t *testing.T) {
	decoded, err := decodeAndValidateAttachmentUploads(
		[]AttachmentUpload{encodedAttachment("hello", 999)},
		5,
		10,
		10,
	)

	require.NoError(t, err)
	require.Len(t, decoded, 1)
	assert.Equal(t, []byte("hello"), decoded[0])
}

// attachmentLogFileService records the stored object name and returns a URL
// that does not repeat the caller's original file name.
type attachmentLogFileService struct {
	interfaces.FileService
	savedName string
}

func (s *attachmentLogFileService) SaveBytes(
	_ context.Context, _ []byte, _ uint64, fileName string, _ bool,
) (string, error) {
	s.savedName = fileName
	return "local://stored-object", nil
}

type attachmentLogModelService struct {
	interfaces.ModelService
	asr asr.ASR
}

func (s *attachmentLogModelService) GetASRModel(context.Context, string) (asr.ASR, error) {
	return s.asr, nil
}

type attachmentLogASR struct {
	err error
}

func (a *attachmentLogASR) Transcribe(context.Context, []byte, string) (*asr.TranscriptionResult, error) {
	return nil, a.err
}

func (a *attachmentLogASR) GetModelName() string { return "" }

func (a *attachmentLogASR) GetModelID() string { return "" }

func captureSessionLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	logger.SetOutput(&buf)
	logger.SetLogLevel(logger.LevelDebug)
	t.Cleanup(logger.ConfigureFromEnv)
	return &buf
}

func TestProcessAttachmentLogOmitsFileName(t *testing.T) {
	const sentinel = "S3NT1NEL"
	fileName := sentinel + ".txt"
	buf := captureSessionLog(t)
	files := &attachmentLogFileService{}
	processor := NewAttachmentProcessor(files, nil, nil, nil)

	att, err := processor.ProcessAttachment(
		context.Background(), []byte("hello\n"), fileName, 6, 1, "",
	)

	require.NoError(t, err)
	require.Equal(t, fileName, att.FileName)
	require.NotContains(t, files.savedName, sentinel)
	out := buf.String()
	require.NotContains(t, out, sentinel)
	require.Contains(t, out, "processing attachment")
	require.Contains(t, out, "attachment processed")
	require.Contains(t, out, "file_name_len=12")
}

func TestProcessAttachmentAudioLogOmitsFileNameAndError(t *testing.T) {
	const sentinel = "S3NT1NEL"
	fileName := sentinel + ".mp3"
	buf := captureSessionLog(t)
	processor := NewAttachmentProcessor(
		&attachmentLogFileService{},
		nil,
		nil,
		&attachmentLogModelService{asr: &attachmentLogASR{err: errors.New("asr " + sentinel)}},
	)

	att, err := processor.ProcessAttachment(
		context.Background(), []byte("audio-bytes"), fileName, 11, 1, "asr-model",
	)

	require.NoError(t, err)
	require.Equal(t, fileName, att.FileName)
	out := buf.String()
	require.NotContains(t, out, sentinel)
	require.Contains(t, out, "starting audio transcription")
	require.Contains(t, out, "audio transcription failed")
	require.Contains(t, out, "file_name_len=12")
	require.Contains(t, out, "error_len=")
}
