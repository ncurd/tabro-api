package service

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestBuildAzureSpeechSSML_EscapesInputAndAppliesVoice(t *testing.T) {
	ssml := buildAzureSpeechSSML(AzureSpeechRequest{
		Input:    `hello <world> & "friends"`,
		Voice:    "zh-CN-XiaoxiaoNeural",
		Language: "zh-CN",
		Speed:    1.2,
	})

	require.Contains(t, ssml, `xml:lang="zh-CN"`)
	require.Contains(t, ssml, `name="zh-CN-XiaoxiaoNeural"`)
	require.Contains(t, ssml, `hello &lt;world&gt; &amp; &#34;friends&#34;`)
	require.Contains(t, ssml, `rate="+20%"`)
}

func TestMapAzureSpeechOutputFormat(t *testing.T) {
	require.Equal(t, "audio-24khz-48kbitrate-mono-mp3", mapAzureSpeechOutputFormat("mp3"))
	require.Equal(t, "riff-24khz-16bit-mono-pcm", mapAzureSpeechOutputFormat("wav"))
	require.Equal(t, "ogg-24khz-16bit-mono-opus", mapAzureSpeechOutputFormat("opus"))
	require.Equal(t, "audio-24khz-48kbitrate-mono-mp3", mapAzureSpeechOutputFormat(""))
}

func TestBuildDashScopeVideoRequest(t *testing.T) {
	body, err := buildDashScopeVideoRequest(VideoGenerationRequest{
		Model:      "happyhorse-1.0-r2v",
		Prompt:     "生成视频",
		Duration:   5,
		Ratio:      "16:9",
		Resolution: "720p",
		Watermark:  boolPtr(false),
		Media: []VideoGenerationMedia{
			{Type: "reference_image", URL: "https://example.com/a.png"},
			{Type: "reference_image", URL: "https://example.com/b.png"},
		},
	})

	require.NoError(t, err)
	require.Equal(t, "happyhorse-1.0-r2v", gjson.GetBytes(body, "model").String())
	require.Equal(t, "生成视频", gjson.GetBytes(body, "input.prompt").String())
	require.Equal(t, "reference_image", gjson.GetBytes(body, "input.media.0.type").String())
	require.Equal(t, "https://example.com/a.png", gjson.GetBytes(body, "input.media.0.url").String())
	require.Equal(t, "reference_image", gjson.GetBytes(body, "input.media.1.type").String())
	require.Equal(t, "https://example.com/b.png", gjson.GetBytes(body, "input.media.1.url").String())
	require.Equal(t, float64(5), gjson.GetBytes(body, "parameters.duration").Float())
	require.Equal(t, "16:9", gjson.GetBytes(body, "parameters.ratio").String())
	require.Equal(t, "720P", gjson.GetBytes(body, "parameters.resolution").String())
	require.False(t, gjson.GetBytes(body, "parameters.watermark").Bool())
}

func TestBuildArkVideoRequest(t *testing.T) {
	body, err := buildArkVideoRequest(VideoGenerationRequest{
		Model:         "doubao-seedance-2-0-260128",
		Prompt:        "生成视频",
		Duration:      8,
		Ratio:         "16:9",
		Watermark:     boolPtr(false),
		GenerateAudio: boolPtr(true),
		Media: []VideoGenerationMedia{
			{Type: "reference_image", URL: "https://example.com/a.png"},
			{Type: "reference_video", URL: "https://example.com/a.mp4"},
			{Type: "reference_audio", URL: "https://example.com/a.mp3"},
		},
	})

	require.NoError(t, err)
	var parsed map[string]any
	require.NoError(t, json.Unmarshal(body, &parsed))
	require.Equal(t, "doubao-seedance-2-0-260128", gjson.GetBytes(body, "model").String())
	require.Equal(t, "text", gjson.GetBytes(body, "content.0.type").String())
	require.Equal(t, "生成视频", gjson.GetBytes(body, "content.0.text").String())
	require.Equal(t, "image_url", gjson.GetBytes(body, "content.1.type").String())
	require.Equal(t, "https://example.com/a.png", gjson.GetBytes(body, "content.1.image_url.url").String())
	require.Equal(t, "reference_image", gjson.GetBytes(body, "content.1.role").String())
	require.Equal(t, "video_url", gjson.GetBytes(body, "content.2.type").String())
	require.Equal(t, "https://example.com/a.mp4", gjson.GetBytes(body, "content.2.video_url.url").String())
	require.Equal(t, "reference_video", gjson.GetBytes(body, "content.2.role").String())
	require.Equal(t, "audio_url", gjson.GetBytes(body, "content.3.type").String())
	require.Equal(t, "https://example.com/a.mp3", gjson.GetBytes(body, "content.3.audio_url.url").String())
	require.Equal(t, "reference_audio", gjson.GetBytes(body, "content.3.role").String())
	require.Equal(t, float64(8), gjson.GetBytes(body, "duration").Float())
	require.Equal(t, "16:9", gjson.GetBytes(body, "ratio").String())
	require.True(t, gjson.GetBytes(body, "watermark").Exists())
	require.False(t, gjson.GetBytes(body, "watermark").Bool())
	require.True(t, gjson.GetBytes(body, "generate_audio").Bool())
}
