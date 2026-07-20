package surfaceadapter

import (
	"context"
	"strings"
	"testing"

	approvalcard "github.com/aos-ref/control-plane/governance/approval-card"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
)

// TestSpan_InteraccaoPorCanalSemSegredos (DoD): cada superfície emite UM span de
// interacção (aos.surface.*) ligado ao trace, com a plataforma/canal/rendered/degraded
// e a classe/irreversível LIDOS — e SEM segredos (nem o preview, nem o resource, nem
// identidades) nos atributos.
func TestSpan_InteraccaoPorCanalSemSegredos(t *testing.T) {
	tracer := &agentruntime.RecordingTracer{}
	// Canal que aprova (reversível, uma aprovação) para exercitar o caminho capaz.
	ch, _ := realChannel(t,
		[]approvalStep{{"operador-a", true}},
		map[string]byte{"operador-a": 1},
	)
	coll, _ := approvalcard.NewDualControlCollector(ch)
	r, _ := RendererFor(PlatformSlack)
	auth, _ := NewSurfaceAuthorizer(r, coll, WithTracer(tracer))

	card := reversibleCard(t)
	if _, _, err := auth.Authorize(context.Background(), card); err != nil {
		t.Fatalf("Authorize: %v", err)
	}

	spans := tracer.SpansByOperation(OpSurfaceInteraction)
	if len(spans) != 1 {
		t.Fatalf("esperava 1 span de interaccao por canal, obtive %d", len(spans))
	}
	s := spans[0]
	if !s.Ended {
		t.Fatal("span de interaccao nao foi fechado")
	}
	if s.Attributes[AttrSurfacePlatform] != string(PlatformSlack) {
		t.Fatalf("plataforma no span incorrecta: %v", s.Attributes[AttrSurfacePlatform])
	}
	if s.Attributes[AttrSurfaceRendered] != true {
		t.Fatalf("rendered no span incorrecto: %v", s.Attributes[AttrSurfaceRendered])
	}
	if s.Attributes[AttrSurfaceDegraded] != false {
		t.Fatalf("degraded no span incorrecto: %v", s.Attributes[AttrSurfaceDegraded])
	}
	// Sem segredos: nenhum atributo string pode conter o preview/resource/identidade.
	for k, v := range s.Attributes {
		str, ok := v.(string)
		if !ok {
			continue
		}
		if strings.Contains(str, card.Preview) || (card.Resource != "" && strings.Contains(str, card.Resource)) {
			t.Fatalf("atributo %q vazou preview/resource: %q", k, str)
		}
		if strings.Contains(str, "operador-a") {
			t.Fatalf("atributo %q vazou identidade: %q", k, str)
		}
	}
}

// TestSpan_DegradadoEmiteSpan (DoD): uma render DEGRADADA também emite o span de
// interacção, com degraded=true.
func TestSpan_DegradadoEmiteSpan(t *testing.T) {
	tracer := &agentruntime.RecordingTracer{}
	ch, _ := realChannel(t, []approvalStep{}, map[string]byte{"x": 1})
	coll, _ := approvalcard.NewDualControlCollector(ch)
	r, _ := RendererFor(PlatformTelegram)
	auth, _ := NewSurfaceAuthorizer(r, coll, WithTracer(tracer))

	if _, _, err := auth.Authorize(context.Background(), irreversibleCard(t)); err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	spans := tracer.SpansByOperation(OpSurfaceInteraction)
	if len(spans) != 1 {
		t.Fatalf("esperava 1 span, obtive %d", len(spans))
	}
	if spans[0].Attributes[AttrSurfaceDegraded] != true {
		t.Fatalf("span de render degradada devia ter degraded=true: %v", spans[0].Attributes[AttrSurfaceDegraded])
	}
}
