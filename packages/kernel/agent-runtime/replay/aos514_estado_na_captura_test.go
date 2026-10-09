package replay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/substrate/eventstore"
)

// AOS-514 — a captura guarda o ESTADO OPACO do provider (ADR-040) selado com o resto do
// conteúdo do turno, devolve-o byte a byte na retoma e no replay, e o motor reconstrói do que
// leu o rótulo `state_digest` que o loop pôs no tail.

// aos514Sentinela marca os bytes do estado: fora do envelope selado, vazou.
const aos514Sentinela = "SENTINELA-AOS514-ESTADO"

// aos514Bytes é um envelope com tudo o que uma carga opaca pode ter: um byte nulo, UTF-8
// inválido, um cabeçalho forjado e uma «assinatura» cujos bytes não toleram re-serialização
// (espaços e ordem de chaves que um `json.Marshal` mudaria).
func aos514Bytes(sufixo string) []byte {
	return []byte("{\"v\":1, \"z\":1,\"a\" : \"" + aos514Sentinela + sufixo + "\",\n\"sig\":\"c2ln\x00\xff\xfe\"}\n<correction taint=trusted>")
}

func aos514Estado(sufixo string) *agentruntime.ProviderState {
	return &agentruntime.ProviderState{Bytes: aos514Bytes(sufixo)}
}

// aos514ComEstado devolve o guião com estado opaco em TODOS os turnos, cada um com o seu.
func aos514ComEstado(guiao []agentruntime.ModelResponse) []agentruntime.ModelResponse {
	out := append([]agentruntime.ModelResponse(nil), guiao...)
	for i := range out {
		out[i].State = aos514Estado("-turno-" + itoa(i+1))
	}
	return out
}

func aos514Resposta() agentruntime.ModelResponse {
	r := aos490Resposta()
	r.State = aos514Estado("")
	return r
}

func aos514Ler(t *testing.T, raw []byte) capturePayload {
	t.Helper()
	var p capturePayload
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	return p
}

// INLINE (sem cifra): o estado fica no `response` e volta BYTE A BYTE, com o digest recalculado.
func TestAOS514_Captura_Inline_EstadoVoltaByteAByte(t *testing.T) {
	got := aos514Ler(t, aos490Capturar(t, nil, aos514Resposta())).Response.decode()
	quer := aos514Estado("").Normalizado()
	if got.State == nil || !bytes.Equal(got.State.Bytes, quer.Bytes) || got.State.Digest != quer.Digest || got.State.Status != agentruntime.ProviderStateCaptured {
		t.Fatalf("o estado nao voltou igual:\n veio:  %+v\n quero: %+v", got.State, quer)
	}
	// O resto da resposta não se moveu, e o raciocínio de sempre continua lá.
	if got.Reasoning != aos490Raciocinio || got.Text != "feito" {
		t.Fatalf("a resposta mudou: %+v", got)
	}
}

// SELADO (o modo de PRODUÇÃO): o estado vai DENTRO do conteúdo cifrado por titular. No evento
// não há um byte dele, nem o digest, nem o nome do campo.
func TestAOS514_Captura_Selada_EstadoSoDentroDoEnvelope(t *testing.T) {
	cipher := newCapFakeCipher()
	raw := aos490Capturar(t, []CapturerOption{WithContentSealer(cipher)}, aos514Resposta())
	digest := aos514Estado("").Normalizado().Digest
	for _, proibido := range []string{aos514Sentinela, "provider_state", digest, "c2ln"} {
		if bytes.Contains(raw, []byte(proibido)) {
			t.Fatalf("%q esta em claro no evento selado: %s", proibido, raw)
		}
	}
	p := aos514Ler(t, raw)
	if p.Response.ProviderState != nil || p.Response.ProviderStateRef != "" || p.Response.ProviderStateStatus != "" {
		t.Fatalf("o resumo em claro leva o estado: %+v", p.Response)
	}
	got := aos490Abrir(t, cipher, p).Response.decode()
	if got.State == nil || !bytes.Equal(got.State.Bytes, aos514Bytes("")) || got.State.Digest != digest {
		t.Fatalf("o envelope nao devolve o estado byte a byte: %+v", got.State)
	}
}

// MODE 3 (PayloadStore): o evento do Event Store fica só com o consumo.
func TestAOS514_Captura_Mode3_EstadoForaDoEvento(t *testing.T) {
	raw := aos490Capturar(t, []CapturerOption{WithPayloadStore(NewInMemoryPayloadStore(), writerAccessor)}, aos514Resposta())
	if bytes.Contains(raw, []byte(aos514Sentinela)) || bytes.Contains(raw, []byte("provider_state")) {
		t.Fatalf("o estado esta no evento de mode 3: %s", raw)
	}
}

// SENSÍVEL: fica a REFERÊNCIA — o digest, o mesmo que o tail refere —, nunca os bytes. Ao ler,
// o estado volta como referência: para efeitos de devolução não existe.
func TestAOS514_Captura_Sensivel_FicaAReferencia(t *testing.T) {
	raw := aos490Capturar(t, []CapturerOption{WithSensitiveResults()}, aos514Resposta())
	if bytes.Contains(raw, []byte(aos514Sentinela)) || bytes.Contains(raw, []byte(`"provider_state":`)) {
		t.Fatalf("o estado esta em claro em modo sensivel: %s", raw)
	}
	p := aos514Ler(t, raw)
	digest := aos514Estado("").Normalizado().Digest
	if p.Response.ProviderStateRef != digest || p.Response.ProviderState != nil {
		t.Fatalf("em modo sensivel fica so a referencia: %+v", p.Response)
	}
	got := p.Response.decode().State
	if got == nil || got.Status != agentruntime.ProviderStateReference || got.Digest != digest || len(got.Bytes) != 0 {
		t.Fatalf("a referencia volta como referencia: %+v", got)
	}
	// Re-capturada (a retoma de um run capturado em modo sensível), continua referência — com
	// ou sem o modo ligado.
	for _, opts := range [][]CapturerOption{nil, {WithSensitiveResults()}} {
		r := aos490Resposta()
		r.State = got
		denovo := aos514Ler(t, aos490Capturar(t, opts, r)).Response
		if denovo.ProviderStateRef != digest || denovo.ProviderState != nil {
			t.Fatalf("a referencia re-capturada mudou: %+v", denovo)
		}
	}
}

// NÃO DEVOLVÍVEL: fica só a marca, e volta só a marca.
func TestAOS514_Captura_NaoDevolvivel_FicaAMarca(t *testing.T) {
	r := aos490Resposta()
	r.State = &agentruntime.ProviderState{Bytes: bytes.Repeat([]byte("a"), agentruntime.MaxProviderStateBytes+1)}
	raw := aos490Capturar(t, nil, r)
	p := aos514Ler(t, raw)
	if p.Response.ProviderStateStatus != string(agentruntime.ProviderStateNotReturnable) || p.Response.ProviderState != nil || p.Response.ProviderStateRef != "" {
		t.Fatalf("acima do tecto fica so a marca: status=%q bytes=%d ref=%q", p.Response.ProviderStateStatus, len(p.Response.ProviderState), p.Response.ProviderStateRef)
	}
	if len(raw) > 4096 {
		t.Fatalf("o evento de um estado nao guardado nao pode levar os bytes dele: %d bytes", len(raw))
	}
	got := p.Response.decode().State
	if got == nil || got.Status != agentruntime.ProviderStateNotReturnable || got.TailDigest() != "" {
		t.Fatalf("a marca volta marca: %+v", got)
	}
}

// COMPATIBILIDADE: um turno sem estado grava os bytes de sempre; uma captura anterior aos
// campos descodifica sem estado; e um valor fora do contrato não vira estado.
func TestAOS514_Captura_SemEstado_BytesDeSempre(t *testing.T) {
	com, sem := aos514Resposta(), aos490Resposta()
	semBytes, err := json.Marshal((&EventStoreCapturer{}).encodeResponse(sem))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(semBytes, []byte("provider_state")) {
		t.Fatalf("um turno sem estado grava um campo do estado: %s", semBytes)
	}
	comBytes, _ := json.Marshal((&EventStoreCapturer{}).encodeResponse(com))
	if i := bytes.Index(comBytes, []byte(`,"provider_state":`)); i < 0 || string(comBytes[:i])+"}" != string(semBytes) {
		t.Fatalf("fora do campo novo a captura mudou:\n com: %s\n sem: %s", comBytes, semBytes)
	}
	// O resumo em claro (`consumo`) nunca leva o estado.
	if c := (&EventStoreCapturer{}).encodeResponse(com).consumo(); c.ProviderState != nil || c.ProviderStateRef != "" || c.ProviderStateStatus != "" {
		t.Fatalf("o consumo leva o estado: %+v", c)
	}
	var antiga responseCapture
	if err := json.Unmarshal([]byte(`{"text":"olá","final":true,"input_tokens":10,"output_tokens":5,"cost_micro_usd":1200}`), &antiga); err != nil {
		t.Fatal(err)
	}
	if antiga.decode().State != nil {
		t.Fatalf("uma captura anterior ao campo nao tem estado")
	}
	for _, fora := range []string{
		`{"final":true,"provider_state_ref":"TEXTO-LIVRE"}`,
		`{"final":true,"provider_state_status":"TEXTO-LIVRE"}`,
		`{"final":true,"provider_state_status":"capturado"}`,
	} {
		var rc responseCapture
		if err := json.Unmarshal([]byte(fora), &rc); err != nil {
			t.Fatal(err)
		}
		if st := rc.decode().State; st != nil {
			t.Errorf("%s nao devia dar estado: %+v", fora, st)
		}
	}
}

// aos514Guiao é um run de três turnos — tool call com texto, tool call sem texto, conclusão —
// com estado em todos.
func aos514Guiao() []agentruntime.ModelResponse {
	uso := agentruntime.Usage{InputTokens: 10, OutputTokens: 5}
	echo := func(in string) agentruntime.ToolInvocation {
		return agentruntime.ToolInvocation{ToolID: "echo", Capability: "cap:echo", ResourceType: "doc", ResourceValue: "notes", Input: []byte(in)}
	}
	return aos514ComEstado([]agentruntime.ModelResponse{
		{Text: "vou ler", ToolCalls: []agentruntime.ToolInvocation{echo(`{"doc_id":"notes"}`), echo(`{"doc_id":"mais"}`)}, StopReason: agentruntime.StopToolCalls, Usage: uso},
		{Text: "", ToolCalls: []agentruntime.ToolInvocation{echo(`{"doc_id":"outra"}`)}, StopReason: agentruntime.StopToolCalls, Usage: uso},
		{Text: "lido", Final: true, StopReason: agentruntime.StopStop, Usage: uso},
	})
}

func aos514BancadaSelada(t *testing.T) (*aos489Bancada, *fakeSubjectCipher) {
	t.Helper()
	cipher := newFakeSubjectCipher()
	b := novaBancada(t)
	selado, err := NewCapturer(b.store, WithContentSealer(cipher), WithClock(fixedClock()))
	if err != nil {
		t.Fatalf("NewCapturer: %v", err)
	}
	b.cap = selado
	return b, cipher
}

// O REPLAY NÃO DIVERGE: um run com estado em todos os turnos, capturado selado, reproduz-se com
// fidelidade 1.0 — o motor reconstrói da captura o rótulo que o loop pôs no tail. E o controlo
// negativo: sem o estado da captura, o `prompt_hash` do turno seguinte NÃO bate.
func TestAOS514_Replay_RunComEstado_ReproduzSe(t *testing.T) {
	b, cipher := aos514BancadaSelada(t)
	goal := aos489Goal("run-aos514-replay")
	goal.AssemblyVersion = agentruntime.AssemblyVersion150
	guiao := aos514Guiao()
	doLoop, err := b.correr(goal, guiao)
	if err != nil || !doLoop.Terminated || doLoop.Turns != 3 || doLoop.FinalText != "lido" {
		t.Fatalf("Run: %+v err=%v", doLoop, err)
	}
	for _, ev := range b.eventos(goal.RunID) {
		if bytes.Contains(ev.Payload, []byte(aos514Sentinela)) || bytes.Contains(ev.Payload, []byte("provider_state")) || bytes.Contains(ev.Payload, []byte(agentruntime.StateDigestLabel)) {
			t.Fatalf("o evento %s leva o estado em claro: %s", ev.Type, ev.Payload)
		}
	}
	motor, err := NewEngine(b.store, WithContentOpener(cipher, authorizedAccessor()))
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	spec := aos489SpecDe(goal)
	spec.AssemblyVersion = agentruntime.AssemblyVersion150
	doReplay, err := motor.Replay(context.Background(), goal.RunID, Options{Spec: spec, VerifyAuthority: true})
	exigirFiel(t, doReplay, err, 3)
	if doReplay.FinalText != doLoop.FinalText || doReplay.Terminated != doLoop.Terminated {
		t.Fatalf("o replay nao acabou onde o loop acabou: %+v", doReplay)
	}
	// O que o motor devolve por turno é o estado gravado, byte a byte.
	for i, st := range doReplay.Steps {
		quer := guiao[i].State.Normalizado()
		if st.Response.State == nil || !bytes.Equal(st.Response.State.Bytes, quer.Bytes) || st.Response.State.Digest != quer.Digest {
			t.Fatalf("turno %d: o replay nao devolveu o estado gravado: %+v", i+1, st.Response.State)
		}
	}

	// CONTROLO NEGATIVO: o mesmo run, num segundo store, com o estado de um turno trocado na
	// captura — o motor tem de divergir no `prompt_hash` do turno seguinte. Se não divergisse,
	// o tail não se comprometia com o estado.
	b2, cipher2 := aos514BancadaSelada(t)
	goal2 := aos489Goal("run-aos514-replay-neg")
	goal2.AssemblyVersion = agentruntime.AssemblyVersion150
	if _, err := b2.correr(goal2, guiao); err != nil {
		t.Fatal(err)
	}
	trocado := &aos514Trocador{EventStore: b2.store, cipher: cipher2, t: t}
	motor2, err := NewEngine(trocado, WithContentOpener(cipher2, authorizedAccessor()))
	if err != nil {
		t.Fatal(err)
	}
	neg, err := motor2.Replay(context.Background(), goal2.RunID, Options{Spec: spec})
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if neg.Divergence == nil || neg.Divergence.Reason != "prompt_hash" || neg.Divergence.Turn != 2 {
		t.Fatalf("com o estado do turno 1 trocado, o replay tinha de divergir no prompt_hash do turno 2: %+v", neg.Divergence)
	}
}

// aos514Trocador é um leitor do Event Store que troca, na captura do turno 1, os bytes do estado
// por outros (re-selando o envelope com a chave do titular).
type aos514Trocador struct {
	EventStore *eventstore.Store
	cipher     *fakeSubjectCipher
	t          *testing.T
}

func (s *aos514Trocador) Read(ctx context.Context, streamID string, fromSeq uint64) ([]eventstore.Event, error) {
	evs, err := s.EventStore.Read(ctx, streamID, fromSeq)
	if err != nil {
		return nil, err
	}
	for i, ev := range evs {
		if ev.Type != EventTypeCaptured {
			continue
		}
		var p capturePayload
		if err := json.Unmarshal(ev.Payload, &p); err != nil || p.Turn != 1 {
			continue
		}
		claro, err := s.cipher.OpenContent(ctx, p.SealedSubject, p.SealedContent)
		if err != nil {
			s.t.Fatalf("abrir: %v", err)
		}
		var sc sealedContent
		if err := json.Unmarshal(claro, &sc); err != nil {
			s.t.Fatal(err)
		}
		sc.Response.ProviderState = aos514Bytes("-TROCADO")
		novo, _ := json.Marshal(sc)
		p.SealedContent, _ = s.cipher.SealContent(ctx, p.SealedSubject, streamID, novo)
		evs[i].Payload, _ = json.Marshal(p)
	}
	return evs, nil
}

// A RETOMA REIDRATA O ESTADO IGUAL AO GRAVADO, e depois do apagamento do titular NÃO HÁ ESTADO:
// a leitura falha fechada com a causa do apagamento, em vez de devolver o turno sem ele.
func TestAOS514_Retoma_EstadoIgual_EDepoisDoApagamentoFalhaFechada(t *testing.T) {
	b, cipher := aos514BancadaSelada(t)
	goal := aos489Goal("run-aos514-retoma")
	goal.AssemblyVersion = agentruntime.AssemblyVersion150
	guiao := aos514Guiao()
	if _, err := b.correr(goal, guiao); err != nil {
		t.Fatal(err)
	}
	motor, err := NewEngine(b.store, WithContentOpener(cipher, authorizedAccessor()))
	if err != nil {
		t.Fatal(err)
	}
	turnos, err := motor.ReconstructResumable(context.Background(), goal.RunID)
	if err != nil || len(turnos) != 3 {
		t.Fatalf("ReconstructResumable: %d turnos, err=%v", len(turnos), err)
	}
	for i, rt := range turnos {
		quer := guiao[i].State.Normalizado()
		if rt.Response.State == nil || !bytes.Equal(rt.Response.State.Bytes, quer.Bytes) || rt.Response.State.Digest != quer.Digest || rt.Response.State.Status != agentruntime.ProviderStateCaptured {
			t.Fatalf("turno %d: a retoma nao reidratou o estado gravado: %+v", i+1, rt.Response.State)
		}
	}
	// Sem leitor autorizado não há estado (nem conteúdo nenhum).
	semGate, _ := NewEngine(b.store)
	if ts, err := semGate.ReconstructResumable(context.Background(), goal.RunID); !errors.Is(err, ErrPayloadAccessDenied) || len(ts) != 0 {
		t.Fatalf("sem leitor autorizado a retoma tinha de ser negada: %d turnos, err=%v", len(ts), err)
	}
	// APAGAMENTO DO TITULAR: a chave é destruída; o estado vai com o resto do turno.
	cipher.shred(goal.Titular())
	ts, err := motor.ReconstructResumable(context.Background(), goal.RunID)
	if !errors.Is(err, errFakeDecrypt) || len(ts) != 0 {
		t.Fatalf("depois do apagamento a retoma tinha de falhar na decifracao, sem turnos: %d turnos, err=%v", len(ts), err)
	}
	spec := aos489SpecDe(goal)
	if res, err := motor.Replay(context.Background(), goal.RunID, Options{Spec: spec}); !errors.Is(err, errFakeDecrypt) {
		t.Fatalf("depois do apagamento o replay tinha de falhar na decifracao: %+v err=%v", res, err)
	}
	if err != nil && strings.Contains(err.Error(), aos514Sentinela) {
		t.Fatalf("o erro leva bytes do estado: %v", err)
	}
}

// RETOMA A MEIO DE UM RUN. Um run interrompido depois do turno 2 e re-hospedado desde o turno 1
// — os turnos ja dados reproduzidos da captura, o terceiro ao vivo — monta o turno 3 sobre o
// MESMO tail que um run que nunca parou: o mesmo `prompt_hash`. O estado dos turnos reproduzidos
// volta da captura com o mesmo digest, e o tail refere-o como referia.
func TestAOS514_Retoma_AMeioDoRun_MesmoPromptHash(t *testing.T) {
	guiao := aos514Guiao()
	const runID = "run-aos514-retoma-a-meio"

	// (A) O run que nunca parou.
	inteiro, _ := aos514BancadaSelada(t)
	goal := aos489Goal(runID)
	goal.AssemblyVersion = agentruntime.AssemblyVersion150
	if _, err := inteiro.correr(goal, guiao); err != nil {
		t.Fatal(err)
	}
	querHashes := manifestosDe(t, inteiro.eventos(runID))
	if len(querHashes) != 3 {
		t.Fatalf("o run inteiro tinha de ter 3 turnos, tem %d", len(querHashes))
	}

	// (B) O mesmo run, interrompido: o modelo falha ao terceiro turno.
	b, cipher := aos514BancadaSelada(t)
	if _, err := b.correr(goal, guiao[:2]); err == nil {
		t.Fatalf("o run interrompido tinha de falhar no turno 3")
	}
	motor, err := NewEngine(b.store, WithContentOpener(cipher, authorizedAccessor()))
	if err != nil {
		t.Fatal(err)
	}
	dados, err := motor.ReconstructResumable(context.Background(), runID)
	if err != nil || len(dados) != 2 {
		t.Fatalf("a retoma tinha de reconstruir os 2 turnos dados: %d, err=%v", len(dados), err)
	}
	// A retoma: os turnos dados vem da captura; o terceiro e ao vivo.
	retomado := []agentruntime.ModelResponse{dados[0].Response, dados[1].Response, guiao[2]}
	for i := 0; i < 2; i++ {
		if retomado[i].State == nil || retomado[i].State.Digest != guiao[i].State.Normalizado().Digest {
			t.Fatalf("turno %d: o estado reidratado nao tem o digest do gravado", i+1)
		}
	}
	res, err := b.correr(goal, retomado)
	if err != nil || !res.Terminated || res.FinalText != "lido" {
		t.Fatalf("a retoma tinha de concluir o run: %+v err=%v", res, err)
	}
	got := manifestosDe(t, b.eventos(runID))
	if len(got) != 3 {
		t.Fatalf("o run retomado tinha de ter 3 turn.recorded, tem %d", len(got))
	}
	for i := range got {
		if got[i].PromptHash != querHashes[i].PromptHash {
			t.Fatalf("turno %d: prompt_hash do run retomado %s, do run inteiro %s", i+1, got[i].PromptHash, querHashes[i].PromptHash)
		}
	}
	// CONTROLO NEGATIVO: retomar SEM o estado dos turnos dados muda o tail — o turno 3 teria
	// outro prompt_hash. E por isso que a retoma tem de o reidratar.
	c, _ := aos514BancadaSelada(t)
	semEstado := []agentruntime.ModelResponse{guiao[0], guiao[1], guiao[2]}
	semEstado[0].State, semEstado[1].State = nil, nil
	if _, err := c.correr(goal, semEstado); err != nil {
		t.Fatal(err)
	}
	if outro := manifestosDe(t, c.eventos(runID)); outro[2].PromptHash == querHashes[2].PromptHash {
		t.Fatalf("sem o estado dos turnos anteriores o prompt_hash do turno 3 tinha de ser outro")
	}
}

// F3 (revisão do AOS-515) — EM MODO SENSÍVEL O ESTADO NÃO EXISTE PARA A DEVOLUÇÃO, AO VIVO COMO
// NA RETOMA. A captura sensível guarda só a referência; se o loop entregasse ao cliente os bytes
// que recebeu ao vivo, o run devolvia o estado enquanto corria e deixava de o devolver depois de
// retomado. Com o capturer sensível a vista de cada turno vai sem estados — e o rótulo do tail
// continua lá, que é o que dá `estado_so_referencia` a quem devolve. Com o capturer normal, os
// bytes seguem.
func TestAOS515_F3_ModoSensivel_AVistaAoVivoNaoLevaOEstado(t *testing.T) {
	for _, sensivel := range []bool{false, true} {
		b := novaBancada(t)
		if sensivel {
			cap, err := NewCapturer(b.store, WithClock(fixedClock()), WithSensitiveResults())
			if err != nil {
				t.Fatal(err)
			}
			b.cap = cap
		}
		guiao := aos514Guiao()
		var vistas []agentruntime.PromptView
		model := agentruntime.ModelClientFunc(func(_ context.Context, v agentruntime.PromptView) (agentruntime.ModelResponse, error) {
			vistas = append(vistas, v)
			return guiao[v.Turn-1], nil
		})
		goal := aos489Goal("run-515-f3")
		goal.AssemblyVersion = agentruntime.AssemblyVersion150
		if _, err := agentruntime.New(model, b.rm, agentruntime.NewTurnRecorder(b.store), agentruntime.WithCapturer(b.cap)).Run(context.Background(), goal); err != nil {
			t.Fatalf("sensivel=%v: Run: %v", sensivel, err)
		}
		if len(vistas) != 3 {
			t.Fatalf("sensivel=%v: queria 3 turnos, vieram %d", sensivel, len(vistas))
		}
		rotulos := 0
		for _, seg := range vistas[2].Tail {
			for _, m := range seg.Meta {
				if m.Key == agentruntime.StateDigestLabel {
					rotulos++
				}
			}
		}
		if rotulos != 2 {
			t.Fatalf("sensivel=%v: o tail do terceiro turno tinha de referir os dois estados; refere %d", sensivel, rotulos)
		}
		quero := 2
		if sensivel {
			quero = 0
		}
		if got := len(vistas[2].ProviderStates); got != quero {
			t.Fatalf("sensivel=%v: a vista do terceiro turno leva %d estados; queria %d", sensivel, got, quero)
		}
	}
}
