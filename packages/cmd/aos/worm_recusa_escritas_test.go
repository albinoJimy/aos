package main

import (
	"context"
	"errors"
	"net/http"
	"testing"

	audit "github.com/aos-ref/platform/audit"
)

// ---------------------------------------------------------------------------------------------
// UM WORM QUE RECUSA ESCRITAS DEIXA DE SER INVISÍVEL.
//
// Achado da verificação de completude de 2026-08-23. Três rotas fail-closed dependem de um
// `Append` e recusavam com o mesmo 503 uniforme, sem log e sem contador. E NADA MAIS SE MOVIA:
//
//	/healthz e /readyz          200
//	aos_worm_partitions         126 (as particoes nascem por run; nenhum run novo entrava)
//	SLI audit_worm_integrity    SEM AMOSTRA (o probeWORM LE, e classifica erro de I/O como
//	                            nao-verificavel de proposito, para nao chamar adulteracao a um
//	                            disco lento)
//
// A avaria real é esta: disco cheio, ou mount remontado `:ro`. As leituras continuam a funcionar
// — e é por isso que o duplo `wormSoLeitura` deste teste aceita `Read`/`Head` e recusa `Append`.
// ---------------------------------------------------------------------------------------------

// ErrDiscoCheio simula a causa típica.
var ErrDiscoCheio = errors.New("no space left on device")

// wormSoLeitura aceita LEITURAS e recusa ESCRITAS — o WORM de um disco cheio.
type wormSoLeitura struct {
	dentro audit.Store
	recusa bool
}

func (w *wormSoLeitura) Append(ctx context.Context, rec audit.AuditRecord) (audit.AuditRecord, error) {
	if w.recusa {
		return audit.AuditRecord{}, ErrDiscoCheio
	}
	return w.dentro.Append(ctx, rec)
}
func (w *wormSoLeitura) Read(ctx context.Context, p string, f, t uint64) ([]audit.AuditRecord, error) {
	return w.dentro.Read(ctx, p, f, t)
}
func (w *wormSoLeitura) Head(ctx context.Context, p string) (uint64, error) {
	return w.dentro.Head(ctx, p)
}
func (w *wormSoLeitura) At(ctx context.Context, p string, s uint64) (audit.AuditRecord, bool, error) {
	return w.dentro.At(ctx, p, s)
}

// NOTA DELIBERADA: este duplo NÃO expõe `Partitions()`. Foi a armadilha que a análise apanhou —
// um decorador que expusesse sempre esse método transformaria um store opaco em «verificável com
// zero partições», e o `Verify` passaria trivialmente. Aqui a ausência é intencional e inócua: o
// que se mede é a escrita.

func noComWormQueRecusa(t *testing.T) (*NodeService, http.Handler, *wormSoLeitura) {
	t.Helper()
	w := &wormSoLeitura{dentro: audit.NewMemStore()}
	cfg := tnBaseConfig()
	cfg.WORM = w
	cfg.BoardRegions = map[string]string{govBoard: govRegion}
	node, err := Bootstrap(context.Background(), cfg, nil)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	t.Cleanup(func() { _ = node.Close() })
	svc, h := newAPI(t, node)
	return svc, h, w
}

func TestUmaSelagemRECUSADAContaEEnvelheceNoMetrics(t *testing.T) {
	svc, h, w := noComWormQueRecusa(t)

	// CONTROLO ANTES: nada falhou ainda, e a idade tem de estar AUSENTE.
	corpo := metricasDe(t, &apiHandler{node: svc.node, svc: svc})
	if v, ok := valorDe(t, corpo, "aos_worm_seal_failures_total"); !ok || v != 0 {
		t.Fatalf("o contador devia sair a 0 antes de qualquer falha; ok=%t v=%v", ok, v)
	}
	if _, ok := valorDe(t, corpo, "aos_worm_seal_last_failure_age_seconds"); ok {
		t.Error("a idade SAIU sem nunca ter havido falha — diria «acabou de falhar» sobre um no sao")
	}

	// O disco enche.
	w.recusa = true
	rec := postJSONComLeitor(t, h, "POST", "/dsar/hold", map[string]any{
		"request_id": "req-worm-1", "subject_id": "nhi-titular",
	}, govReader)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("com o WORM a recusar escritas, /dsar/hold devia dar 503; veio %d", rec.Code)
	}

	corpo = metricasDe(t, &apiHandler{node: svc.node, svc: svc})
	if v, _ := valorDe(t, corpo, "aos_worm_seal_failures_total"); v < 1 {
		t.Errorf("a selagem recusada NAO foi contada (%v) — o no recusa 100%% dos legal holds e "+
			"nenhuma serie se move", v)
	}
	if _, ok := valorDe(t, corpo, "aos_worm_seal_last_failure_age_seconds"); !ok {
		t.Error("a idade da ultima falha NAO saiu — o contador sozinho nao distingue «falhou e " +
			"recuperou» de «esta a falhar agora»")
	}
}

// TestOReadyzFICA_VERMELHO_ComOWormARecusar é a metade que interessa ao orquestrador.
//
// Um nó que recusa submissões, leituras sensíveis e legal holds não está pronto a servir, por
// mais que o `/healthz` responda.
func TestOReadyzFICA_VERMELHO_ComOWormARecusar(t *testing.T) {
	_, h, w := noComWormQueRecusa(t)

	// CONTROLO ANTES: verde.
	if rec := getReq(h, "/readyz", nil); rec.Code != http.StatusOK {
		t.Fatalf("antes da falha o /readyz devia estar VERDE; veio %d (%s)", rec.Code, rec.Body.String())
	}

	w.recusa = true
	if rec := postJSONComLeitor(t, h, "POST", "/dsar/hold", map[string]any{
		"request_id": "req-worm-2", "subject_id": "nhi-titular",
	}, govReader); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("o legal hold devia recusar; veio %d", rec.Code)
	}

	if rec := getReq(h, "/readyz", nil); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("o /readyz continua VERDE com o WORM a recusar escritas — o orquestrador continua "+
			"a encaminhar para um no que recusa 100%% das submissoes; veio %d", rec.Code)
	}
	// E o /healthz NAO muda: distinguir vivo de pronto é o propósito das duas sondas.
	if rec := getReq(h, "/healthz", nil); rec.Code != http.StatusOK {
		t.Errorf("o /healthz nao devia mudar — o processo esta vivo; veio %d", rec.Code)
	}
}

// TestUmaSelagemBEM_SUCEDIDA_LimpaAProntidao — auto-curativo, e é a âncora anti-vacuidade.
//
// Sem ela, «vermelho para sempre depois da primeira falha» passaria no teste acima, e um soluço
// de disco deixaria o nó fora de rotação até alguém o reiniciar.
func TestUmaSelagemBEM_SUCEDIDA_LimpaAProntidao(t *testing.T) {
	svc, h, w := noComWormQueRecusa(t)

	w.recusa = true
	_ = postJSONComLeitor(t, h, "POST", "/dsar/hold", map[string]any{
		"request_id": "req-worm-3", "subject_id": "nhi-titular",
	}, govReader)
	if rec := getReq(h, "/readyz", nil); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("pre-condicao: o /readyz devia estar vermelho; veio %d", rec.Code)
	}

	// O disco esvazia.
	w.recusa = false
	if rec := postJSONComLeitor(t, h, "POST", "/dsar/hold", map[string]any{
		"request_id": "req-worm-4", "subject_id": "nhi-titular",
	}, govReader); rec.Code != http.StatusOK {
		t.Fatalf("com o WORM a aceitar, o legal hold devia passar; veio %d (%s)", rec.Code, rec.Body.String())
	}
	if rec := getReq(h, "/readyz", nil); rec.Code != http.StatusOK {
		t.Errorf("uma selagem BEM-SUCEDIDA nao limpou a prontidao — um solucco de disco deixa o no "+
			"fora de rotacao ate alguem o reiniciar; veio %d", rec.Code)
	}
	// E o contador NÃO recua: é cumulativo, e o incidente aconteceu.
	corpo := metricasDe(t, &apiHandler{node: svc.node, svc: svc})
	if v, _ := valorDe(t, corpo, "aos_worm_seal_failures_total"); v < 1 {
		t.Errorf("o contador recuou depois da recuperacao (%v) — um contador que esquece esconde "+
			"o incidente de quem o investiga depois", v)
	}
}
