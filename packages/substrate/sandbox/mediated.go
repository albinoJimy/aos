package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"unicode/utf8"

	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
)

// Authorization transporta a identidade e o alvo que o Reference Monitor avalia
// para uma execução na sandbox (ADR-002/003). É separada do [ExecRequest] para
// que o núcleo do driver permaneça desacoplado do RM: só o [MediatedLauncher] os
// combina, ao montar o [referencemonitor.Call].
type Authorization struct {
	// Principal é a NHI que origina o efeito (resolvida/validada pelo hook de
	// identidade do RM).
	Principal referencemonitor.Principal
	// Capability é o direito escopado que a política avalia (ex.: "cap:http.get").
	Capability string
	// Resource é o alvo concreto do efeito (contrato C1 do RM).
	Resource referencemonitor.Resource
	// Credential é o token NHI apresentado ao RM (bearer efémero; não é o segredo
	// downstream — esse resolve-se por handle server-side, ADR-006).
	Credential string
}

// MediatedLauncher é o ÚNICO adaptador exportado que corre a sandbox — e fá-lo
// SEMPRE através do Reference Monitor. Na construção regista o seu despacho como
// [referencemonitor.ToolFunc] no RM; a partir daí a única via de execução é
// [referencemonitor.Monitor.Mediate]. A superfície pública [MediatedLauncher.Execute]
// limita-se a chamar Mediate — nunca toca no [Launcher] directamente. Assim, "a
// invocação da sandbox só pode partir do Reference Monitor" é estrutural (ADR-002).
type MediatedLauncher struct {
	rm       *referencemonitor.Monitor
	launcher *Launcher
	toolID   string
}

// NewMediatedLauncher liga o launcher ATRÁS do Reference Monitor: regista o
// despacho da sandbox como a ToolFunc do toolID dado. Falha se o toolID já estiver
// registado (o RM é imutável no registo) ou se faltarem argumentos.
func NewMediatedLauncher(rm *referencemonitor.Monitor, launcher *Launcher, toolID string) (*MediatedLauncher, error) {
	if rm == nil {
		return nil, ErrNilMonitor
	}
	if launcher == nil {
		return nil, ErrNilDriver
	}
	if toolID == "" {
		return nil, ErrEmptyToolID
	}
	ml := &MediatedLauncher{rm: rm, launcher: launcher, toolID: toolID}
	if err := rm.Register(toolID, ml.dispatch); err != nil {
		return nil, err
	}
	return ml, nil
}

// ToolID devolve o id sob o qual a sandbox está registada no RM.
func (ml *MediatedLauncher) ToolID() string { return ml.toolID }

// dispatch é a [referencemonitor.ToolFunc] registada no RM. É NÃO-EXPORTADA: nenhum
// pacote externo a alcança; só o dispatcher interno do RM a invoca, e só sob um
// permit não-forjável válido (ver reference-monitor/monitor.go). Descodifica o
// envelope de execução e corre o ciclo de vida guardado ([Launcher.run]). O
// resultado é serializado com o taint untrusted implícito (o descodificador força-o).
func (ml *MediatedLauncher) dispatch(ctx context.Context, input []byte) ([]byte, error) {
	var req ExecRequest
	if err := json.Unmarshal(input, &req); err != nil {
		return nil, err
	}
	res, err := ml.launcher.run(ctx, req)
	if err != nil {
		return nil, err
	}
	return encodeResult(res)
}

// Execute é a entrada pública que o runtime chama. Encaminha o pedido ATRAVÉS do
// Reference Monitor (rm.Mediate) — NUNCA corre a sandbox directamente. Numa
// decisão que não seja permit devolve [*DeniedError] (nenhum efeito ocorreu). Num
// permit, devolve o [ExecResult] SEMPRE untrusted (ADR-005) e, quando a execução
// na sandbox falhou apesar do permit, o erro dessa execução.
func (ml *MediatedLauncher) Execute(ctx context.Context, authz Authorization, req ExecRequest) (ExecResult, error) {
	input, err := json.Marshal(req)
	if err != nil {
		return ExecResult{}, err
	}
	call := referencemonitor.Call{
		RunID:      req.RunID,
		StepID:     req.StepID,
		ToolID:     ml.toolID,
		Capability: authz.Capability,
		Resource:   authz.Resource,
		Principal:  authz.Principal,
		Credential: authz.Credential,
		Context:    referencemonitor.CallContext{Taint: string(TaintUntrusted)},
		Input:      input,
	}
	dec, err := ml.rm.Mediate(ctx, call)
	if err != nil {
		return ExecResult{}, err // cancelamento de contexto
	}
	if dec.Effect != referencemonitor.EffectPermit {
		return ExecResult{}, &DeniedError{Effect: string(dec.Effect), Code: dec.Code, Reason: dec.Reason}
	}
	if dec.ToolErr != nil {
		// O RM permitiu e despachou, mas a execução na sandbox falhou (ex.: escape
		// bloqueado). O efeito não produziu resultado; o resultado é untrusted-vazio.
		return ExecResult{}, dec.ToolErr
	}
	return decodeResult(dec.Output)
}

// resultDTO é a serialização do [ExecResult] entre o despacho e o Execute. O taint
// NÃO é serializado: é reimposto (untrusted) na descodificação — não há como o
// tornar trusted.
//
// É TAMBÉM o que o modelo lê: o output da ToolFunc é o resultado da tool call que o
// ciclo do runtime põe no tail do prompt, tal como vem. Por isso o `stdout` que é
// texto (UTF-8 válido) viaja em `stdout_text`, como texto (AOS-487). Antes ia sempre
// em `stdout`, um `[]byte`, que o JSON codifica em base64: o modelo recebia o
// documento que pediu em base64, tentava validá-lo com uma segunda leitura (negada por
// taint) e, em produção, chegou a desistir com o documento no contexto. O `stdout`
// binário continua em `stdout` (base64), e é também a forma dos resultados gravados
// antes desta versão — o descodificador lê as duas.
type resultDTO struct {
	StdoutText string     `json:"stdout_text,omitempty"`
	Stdout     []byte     `json:"stdout,omitempty"`
	Artifacts  []Artifact `json:"artifacts,omitempty"`
	ExitCode   int        `json:"exit_code"`
}

// ErrAmbiguousResult — um resultado serializado com o `stdout` nos dois campos. O
// codificador nunca o produz; descodificá-lo obrigava a escolher um dos dois em
// silêncio.
var ErrAmbiguousResult = errors.New("sandbox: resultado com stdout_text e stdout ao mesmo tempo")

func encodeResult(r ExecResult) ([]byte, error) {
	dto := resultDTO{Artifacts: r.Artifacts, ExitCode: r.ExitCode}
	if utf8.Valid(r.Stdout) {
		dto.StdoutText = string(r.Stdout)
	} else {
		dto.Stdout = r.Stdout
	}
	// Sem o escape de HTML do json.Marshal: `<`, `>` e `&` de um documento chegariam ao
	// modelo como `<`, `>` e `&`. O Encode acrescenta um `\n` final, que
	// sai — os bytes ficam os do json.Marshal, salvo o escape.
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(dto); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

func decodeResult(b []byte) (ExecResult, error) {
	if len(b) == 0 {
		return ExecResult{}, nil // untrusted por construção
	}
	var dto resultDTO
	if err := json.Unmarshal(b, &dto); err != nil {
		return ExecResult{}, err
	}
	if dto.StdoutText != "" && len(dto.Stdout) > 0 {
		return ExecResult{}, ErrAmbiguousResult
	}
	stdout := dto.Stdout
	if dto.StdoutText != "" {
		stdout = []byte(dto.StdoutText)
	}
	// o taint é reimposto pelo tipo (sempre untrusted)
	return ExecResult{Stdout: stdout, Artifacts: dto.Artifacts, ExitCode: dto.ExitCode}, nil
}
