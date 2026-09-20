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

func TestBuildWan3VideoRequestPreservesMultimodalInputs(t *testing.T) {
	var req VideoGenerationRequest
	require.NoError(t, json.Unmarshal([]byte(`{"model":"wan3.0-video","duration":-1,"generate_audio":false,"prompt_extend":true,"resolution":"480p","media":[{"type":"reference_image","url":"https://example.com/image.png"},{"type":"reference_video","url":"https://example.com/video.mp4"},{"type":"reference_audio","url":"https://example.com/audio.mp3"},{"type":"file","url":"https://example.com/brief.pdf"}]}`), &req))
	body, err := buildDashScopeVideoRequest(req)
	require.NoError(t, err)
	require.False(t, gjson.GetBytes(body, "input.prompt").Exists())
	require.Equal(t, int64(4), gjson.GetBytes(body, "input.media.#").Int())
	require.Equal(t, "reference_video", gjson.GetBytes(body, "input.media.1.type").String())
	require.Equal(t, "reference_audio", gjson.GetBytes(body, "input.media.2.type").String())
	require.Equal(t, "file", gjson.GetBytes(body, "input.media.3.type").String())
	require.Equal(t, int64(-1), gjson.GetBytes(body, "parameters.duration").Int())
	require.True(t, gjson.GetBytes(body, "parameters.audio").Exists())
	require.False(t, gjson.GetBytes(body, "parameters.audio").Bool())
	require.True(t, gjson.GetBytes(body, "parameters.prompt_extend").Bool())
	require.Equal(t, "480P", gjson.GetBytes(body, "parameters.resolution").String())
}

func TestBuildSeedance25VideoRequestPreservesOptions(t *testing.T) {
	var req VideoGenerationRequest
	require.NoError(t, json.Unmarshal([]byte(`{"model":"doubao-seedance-2-5-260628","prompt":"Extend this shot","duration":-1,"generate_audio":false,"return_last_frame":true,"output_format":"mov","omni_reference_task_type":"extend","resolution":"720P","media":[{"type":"reference_video","url":"asset://source"}]}`), &req))
	body, err := buildArkVideoRequest(req)
	require.NoError(t, err)
	require.Equal(t, "doubao-seedance-2-5-260628", gjson.GetBytes(body, "model").String())
	require.Equal(t, int64(-1), gjson.GetBytes(body, "duration").Int())
	require.True(t, gjson.GetBytes(body, "generate_audio").Exists())
	require.False(t, gjson.GetBytes(body, "generate_audio").Bool())
	require.True(t, gjson.GetBytes(body, "return_last_frame").Bool())
	require.Equal(t, "mov", gjson.GetBytes(body, "output_format").String())
	require.Equal(t, "extend", gjson.GetBytes(body, "omni_reference_task_type").String())
	require.Equal(t, "720p", gjson.GetBytes(body, "resolution").String())
	require.Equal(t, "asset://source", gjson.GetBytes(body, "content.1.video_url.url").String())
}

func TestBuildVideoRequestPreservesFirstAndLastFrames(t *testing.T) {
	req := VideoGenerationRequest{Model: "model", Media: []VideoGenerationMedia{{Type: "first_frame", URL: "https://example.com/first.png"}, {Type: "last_frame", URL: "https://example.com/last.png"}}}
	body, err := buildArkVideoRequest(req)
	require.NoError(t, err)
	require.Equal(t, "first_frame", gjson.GetBytes(body, "content.0.role").String())
	require.Equal(t, "last_frame", gjson.GetBytes(body, "content.1.role").String())
	body, err = buildDashScopeVideoRequest(req)
	require.NoError(t, err)
	require.Equal(t, "first_frame", gjson.GetBytes(body, "input.media.0.type").String())
	require.Equal(t, "last_frame", gjson.GetBytes(body, "input.media.1.type").String())
}

func TestVideoRequestsRejectInvalidMediaInsteadOfDroppingIt(t *testing.T) {
	for _, req := range []VideoGenerationRequest{
		{Model: "model"},
		{Model: "model", Prompt: "test", Duration: -2},
		{Model: "model", Media: []VideoGenerationMedia{{Type: "unknown", URL: "https://example.com/ref"}}},
		{Model: "model", Media: []VideoGenerationMedia{{Type: "reference_image"}}},
	} {
		_, err := buildArkVideoRequest(req)
		require.Error(t, err)
		_, err = buildDashScopeVideoRequest(req)
		require.Error(t, err)
	}
	_, err := buildArkVideoRequest(VideoGenerationRequest{Model: "model", Media: []VideoGenerationMedia{{Type: "file", URL: "https://example.com/brief.pdf"}}})
	require.ErrorContains(t, err, "unsupported Ark media type")
}

func TestAzureSpeechRequestJSONTags(t *testing.T) {
	var req AzureSpeechRequest
	require.NoError(t, json.Unmarshal([]byte(`{"model":"tts-1","input":"test","response_format":"wav"}`), &req))
	require.Equal(t, "wav", req.ResponseFormat)
}

func TestVideoProvidersPreserveInlineImageAndRejectUnsupportedBase64(t *testing.T) {
	image := "data:image/png;base64,aW1hZ2U="
	for _, mediaType := range []string{"first_frame", "last_frame", "reference_image"} {
		req := VideoGenerationRequest{Model: "wan3.0-video", Media: []VideoGenerationMedia{{Type: mediaType, URL: image}}}
		body, err := buildDashScopeVideoRequest(req)
		require.NoError(t, err)
		require.Equal(t, image, gjson.GetBytes(body, "input.media.0.url").String())
		req.Model = "doubao-seedance-2-5-260628"
		body, err = buildArkVideoRequest(req)
		require.NoError(t, err)
		require.Equal(t, image, gjson.GetBytes(body, "content.0.image_url.url").String())
	}
	audio := "data:audio/mpeg;base64,YXVkaW8="
	req := VideoGenerationRequest{Model: "doubao-seedance-2-5-260628", Media: []VideoGenerationMedia{{Type: "reference_audio", URL: audio}}}
	body, err := buildArkVideoRequest(req)
	require.NoError(t, err)
	require.Equal(t, audio, gjson.GetBytes(body, "content.0.audio_url.url").String())
	_, err = buildDashScopeVideoRequest(req)
	require.ErrorContains(t, err, "public HTTP(S) URL")
	var invalid *InvalidMediaRequestError
	require.ErrorAs(t, err, &invalid)

	for _, provider := range []func(VideoGenerationRequest) ([]byte, error){buildArkVideoRequest, buildDashScopeVideoRequest} {
		for _, media := range []VideoGenerationMedia{
			{Type: "reference_video", URL: "data:video/mp4;base64,dmlkZW8="},
			{Type: "reference_image", URL: "data:image/png;base64,!!!"},
			{Type: "reference_image", URL: "data:text/html;base64,YQ=="},
			{Type: "reference_image", URL: "data:image/svg+xml;base64,YQ=="},
			{Type: "reference_image", URL: "file:///tmp/image.png"},
			{Type: "reference_image", URL: "https://localhost/image.png"},
			{Type: "file", URL: "data:application/pdf;base64,YQ=="},
		} {
			_, err := provider(VideoGenerationRequest{Model: "model", Media: []VideoGenerationMedia{media}})
			require.ErrorAs(t, err, &invalid, media.Type)
		}
	}
}

func TestDashScopeRejectsArkAssetReferences(t *testing.T) {
	_, err := buildDashScopeVideoRequest(VideoGenerationRequest{Model: "wan3.0-video", Media: []VideoGenerationMedia{{Type: "reference_image", URL: "asset://source"}}})
	require.ErrorContains(t, err, "public HTTP(S)")
}
