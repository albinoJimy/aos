package replay

import (
	"bytes"
	"context"
	"errors"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
)

// AOS-546 — o replay de um run da 1.7.0 com media reproduz-se SEM os bytes: o segmento `media` é
// função do resultado capturado, e o `prompt_hash` compromete-se com a imagem pelo digest.
func TestAOS546_ReplayDeUmRunComMediaSemOsBytes(t *testing.T) {
	b := novaBancada(t)
	ref, err := agentruntime.MediaRef{
		Doc: "sala.png", SHA256: "sha256:" + string(bytes.Repeat([]byte("ab"), 32)), MediaType: "image/png", Bytes: 2048,
	}.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if err := b.rm.Register("media_view", func(context.Context, []byte) ([]byte, error) { return ref, nil }); err != nil {
		t.Fatalf("Register media: %v", err)
	}
	goal := aos489Goal("run-aos546-replay-media")
	goal.AssemblyVersion = agentruntime.AssemblyVersion170
	goal.Tools = append(goal.Tools, agentruntime.ToolSpec{Name: "media_view", Version: "1.0.0", Digest: "sha256:ee05"})
	uso := agentruntime.Usage{InputTokens: 10, OutputTokens: 5}
	guiao := []agentruntime.ModelResponse{
		{Text: "vou ver a foto", ToolCalls: []agentruntime.ToolInvocation{{ToolID: "media_view", Capability: "cap:echo", ResourceType: "doc", ResourceValue: "notes", Input: []byte(`{"doc_id":"sala.png"}`)}}, StopReason: agentruntime.StopToolCalls, Usage: uso},
		{Text: "uma sala com dois quartos", Final: true, Usage: uso},
	}
	var segundo []byte
	model := agentruntime.ModelClientFunc(func(_ context.Context, v agentruntime.PromptView) (agentruntime.ModelResponse, error) {
		if v.Turn < 1 || v.Turn > len(guiao) {
			return agentruntime.ModelResponse{}, errors.New("guiao esgotado")
		}
		if v.Turn == 2 {
			segundo = append([]byte(nil), v.Materialized...)
		}
		return guiao[v.Turn-1], nil
	})
	res, err := agentruntime.New(model, b.rm, agentruntime.NewTurnRecorder(b.store), agentruntime.WithCapturer(b.cap)).Run(context.Background(), goal)
	if err != nil || !res.Terminated || res.Turns != 2 {
		t.Fatalf("Run: %+v, %v", res, err)
	}
	if !bytes.Contains(segundo, []byte("<media taint=untrusted media_doc=sala.png media_sha256=sha256:abab")) {
		t.Fatalf("o segundo turno nao viu o segmento media:\n%s", segundo)
	}
	spec := aos489SpecDe(goal)
	spec.AssemblyVersion = agentruntime.AssemblyVersion170
	doReplay, err := b.motor().Replay(context.Background(), goal.RunID, Options{Spec: spec})
	exigirFiel(t, doReplay, err, 2)
}
