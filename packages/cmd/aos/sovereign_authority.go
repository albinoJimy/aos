// AOS-205 — FONTE DE AUTORIDADE da soberania de leitura (board→região). Fecha o eixo que
// os deferimentos DEF-201/203/205/209/211 nomeavam: o registo board→região deixa de ser o
// mapa ESTÁTICO de `AOS_BOARD_REGIONS` tratado COMO SE fosse a autoridade da organização e
// passa a viver atrás de uma PORTA DE AUTORIDADE com duas propriedades que um mapa de env não
// tem — ROTAÇÃO (o conjunto board→região pode ser re-provisionado em runtime) e AUDITORIA DE
// ALTERAÇÕES (cada provisionamento/rotação é SELADO na hash-chain WORM tamper-evident, sem PII).
//
// O QUE MUDA E O QUE NÃO MUDA. A REGRA fail-closed board→região NÃO se duplica: continua a ser
// [govsov.Registry] (AOS-094/ADR-011) — RegionFor resolve, board desconhecido ⇒ deny. O que
// muda é a FONTE: em vez de o env ser lido como verdade e congelado no arranque, o env é apenas
// a SEMENTE do provisionamento inicial, e a autoridade regista cada alteração com uma revisão
// monotónica e um selo verificável. Um operador que muda a fronteira de um board deixa um trilho
// tamper-evidente; o mapa de env não deixava nenhum.
//
// FRONTEIRA REALISTA (declarada — análogo ao tratamento de D4/AOS-16x para identidade). O que
// este ficheiro ENTREGA é o CONTRATO da fonte de autoridade: um registo rotacionável e auditado.
// O TENANT concreto — a integração com o serviço de configuração/IdP de soberania REAL da
// organização que empurra as alterações autoritativas — é infra-org, não código do nó, e fica
// DEFERIDA: a semente e as rotações entram por config/API do operador, não de um tenant real.
// É DEMO-GRADE nesse sentido preciso, e o banner do arranque declara-o sem eufemismo.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	govsov "github.com/aos-ref/control-plane/governance/sovereignty"
	audit "github.com/aos-ref/platform/audit"
)

// Partição e vocabulário do trilho de auditoria da autoridade de soberania (sem PII: boards e
// regiões são identificadores de GOVERNAÇÃO, nunca dados pessoais).
const (
	// sovereignAuthorityPartition é a cadeia WORM dedicada às alterações do registo board→região.
	// Namespaced para não colidir com a mediação do RM nem com os selos de leitura (gov.read/…).
	sovereignAuthorityPartition = "gov.sovereignty.authority"
	// sovereignAuthorityToolID identifica o produtor dos selos de alteração (ToolID), sem PII.
	sovereignAuthorityToolID = "gov.sovereignty"
	// capSovereignProvision / capSovereignRotate rotulam o selo do provisionamento inicial e o de
	// uma rotação subsequente — vocabulário estável, verificável na cadeia.
	capSovereignProvision = "gov.sovereignty.provision"
	capSovereignRotate    = "gov.sovereignty.rotate"

	// Obrigações que carregam os METADADOS da alteração no selo (nunca PII): a revisão
	// monotónica, o nº de boards e a impressão digital do conjunto board→região (antes/depois).
	obSovereignRevision    = "gov.sovereignty.revision"
	obSovereignBoardCount  = "gov.sovereignty.board_count"
	obSovereignFingerprint = "gov.sovereignty.fingerprint"
	obSovereignPrevFinger  = "gov.sovereignty.prev_fingerprint"
)

// ErrNilAuthorityWORM — a autoridade de soberania EXIGE um WORM onde selar as alterações. Sem
// ele não haveria auditoria de alterações — precisamente a propriedade que a distingue do mapa
// de env. Fail-closed: recusa a construção.
var ErrNilAuthorityWORM = errors.New("aos: SovereignRegionAuthority exige um audit.Store (WORM) para a auditoria de alteracoes")

// SovereignRegionAuthority é a FONTE DE AUTORIDADE board→região (AOS-205): um [govsov.Registry]
// rotacionável cujas alterações são seladas no WORM. Concorrente-seguro (RWMutex): RegionFor é
// leitura pura; Rotate troca o registo atomicamente e encadeia um selo. É o que o read-path
// soberano consulta em vez do mapa estático de env.
type SovereignRegionAuthority struct {
	mu          sync.RWMutex
	current     *govsov.Registry
	fingerprint string // impressão digital do conjunto board→região EM VIGOR (hex sha256)
	revision    uint64 // revisão monotónica (0 = provisionamento inicial)

	worm audit.Store
	now  func() time.Time
}

// NewSovereignRegionAuthority provisiona a autoridade a partir da SEMENTE seed (o mapa
// board→região inicial — tipicamente de AOS_BOARD_REGIONS, mas tratado como semente de
// provisionamento, não como verdade congelada) e SELA o provisionamento inicial (revisão 0) no
// WORM. Fail-closed: worm nil ⇒ [ErrNilAuthorityWORM] (sem auditoria não é autoridade). now nil
// cai em time.Now.
func NewSovereignRegionAuthority(ctx context.Context, seed map[string]string, worm audit.Store, now func() time.Time) (*SovereignRegionAuthority, error) {
	if worm == nil {
		return nil, ErrNilAuthorityWORM
	}
	if now == nil {
		now = time.Now
	}
	reg := govsov.NewRegistry(seed)
	a := &SovereignRegionAuthority{
		current:     reg,
		fingerprint: fingerprintRegions(seed),
		revision:    0,
		worm:        worm,
		now:         now,
	}
	if err := a.seal(ctx, capSovereignProvision, reg.Len(), a.fingerprint, ""); err != nil {
		return nil, err
	}
	return a, nil
}

// RegionFor resolve a região autorizada de um board pela autoridade EM VIGOR — a MESMA regra
// fail-closed de [govsov.Registry.RegionFor] (board vazio/desconhecido ⇒ ("", false)), sobre o
// registo corrente (que uma rotação pode ter substituído). NÃO se duplica a regra.
func (a *SovereignRegionAuthority) RegionFor(board string) (string, bool) {
	a.mu.RLock()
	reg := a.current
	a.mu.RUnlock()
	return reg.RegionFor(board)
}

// Registry devolve o [govsov.Registry] EM VIGOR (snapshot imutável) — útil para os consumidores
// que só precisam da regra board→região corrente (ex.: o predicado do banner). O registo é
// imutável após construção, pelo que a partilha é segura.
func (a *SovereignRegionAuthority) Registry() *govsov.Registry {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.current
}

// Rotate RE-PROVISIONA o conjunto board→região a partir de newSeed e SELA a alteração no WORM
// (revisão++), atribuindo-a a actor. É a operação que um mapa de env não tem: a fronteira de um
// board pode mudar em runtime, e a mudança fica tamper-evidente. Devolve a nova revisão. Um
// newSeed IDÊNTICO ao actual ainda regista uma rotação (a intenção de re-provisionar é auditável
// mesmo quando o conjunto não muda). Fail-closed: se o selo NÃO encadear, a rotação é ABORTADA e
// o registo EM VIGOR não é trocado (não se muda a fronteira sem deixar trilho).
func (a *SovereignRegionAuthority) Rotate(ctx context.Context, newSeed map[string]string, actor string) (uint64, error) {
	newReg := govsov.NewRegistry(newSeed)
	newFinger := fingerprintRegions(newSeed)

	a.mu.Lock()
	defer a.mu.Unlock()
	prevFinger := a.fingerprint
	nextRev := a.revision + 1
	// SELA ANTES de trocar: a auditoria da alteração é PRÉ-CONDIÇÃO da alteração (fail-closed).
	if err := a.sealLocked(ctx, capSovereignRotate, nextRev, newReg.Len(), newFinger, prevFinger, actor); err != nil {
		return a.revision, err
	}
	a.current = newReg
	a.fingerprint = newFinger
	a.revision = nextRev
	return nextRev, nil
}

// Revision devolve a revisão monotónica EM VIGOR (0 = provisionamento inicial).
func (a *SovereignRegionAuthority) Revision() uint64 {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.revision
}

// Fingerprint devolve a impressão digital do conjunto board→região EM VIGOR (hex sha256) — o
// que o selo grava, para verificação de que a fronteira em vigor é a que foi auditada.
func (a *SovereignRegionAuthority) Fingerprint() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.fingerprint
}

// Len devolve o nº de boards no registo EM VIGOR (observabilidade/banner).
func (a *SovereignRegionAuthority) Len() int {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.current.Len()
}

// seal encadeia um selo de alteração com o revision EM VIGOR (usado pelo provisionamento
// inicial, ainda sem lock detido pelo chamador).
func (a *SovereignRegionAuthority) seal(ctx context.Context, capability string, boardCount int, finger, prevFinger string) error {
	return a.appendSeal(ctx, capability, a.revision, boardCount, finger, prevFinger, "gov.sovereignty.bootstrap")
}

// sealLocked encadeia um selo de rotação (o chamador detém o lock de escrita).
func (a *SovereignRegionAuthority) sealLocked(ctx context.Context, capability string, revision uint64, boardCount int, finger, prevFinger, actor string) error {
	if strings.TrimSpace(actor) == "" {
		actor = "gov.sovereignty.operator"
	}
	return a.appendSeal(ctx, capability, revision, boardCount, finger, prevFinger, actor)
}

// appendSeal grava UM registo tamper-evidente da alteração — SEM PII (revisão, nº de boards e
// impressões digitais são metadados de governação). O selo é a prova de "o que a autoridade
// afirmava em cada revisão", verificável isoladamente na sua partição.
func (a *SovereignRegionAuthority) appendSeal(ctx context.Context, capability string, revision uint64, boardCount int, finger, prevFinger, actor string) error {
	obligations := []audit.Obligation{
		{Type: obSovereignRevision, Fields: []string{uitoa(revision)}},
		{Type: obSovereignBoardCount, Fields: []string{itoa(boardCount)}},
		{Type: obSovereignFingerprint, Fields: []string{finger}},
	}
	if prevFinger != "" {
		obligations = append(obligations, audit.Obligation{Type: obSovereignPrevFinger, Fields: []string{prevFinger}})
	}
	rec := audit.AuditRecord{
		Partition:   sovereignAuthorityPartition,
		Timestamp:   a.now().UTC(),
		Decision:    audit.DecisionAllow,
		Principal:   audit.Principal{NHIID: actor},
		Capability:  capability,
		ToolID:      sovereignAuthorityToolID,
		Resource:    audit.Resource{Type: "sovereignty.board_regions", Value: finger},
		Obligations: obligations,
	}
	_, err := a.worm.Append(ctx, rec)
	return err
}

// fingerprintRegions computa a impressão digital determinista do conjunto board→região: sha256
// sobre as entradas NORMALIZADAS e ORDENADAS ("board=regiao\n"), no mesmo espírito de
// normalização de [govsov] (aparado + caixa reduzida). Entradas com board/região vazios são
// descartadas (coerente com [govsov.NewRegistry]). Determinista ⇒ a mesma fronteira dá sempre a
// mesma impressão, e uma alteração muda-a.
func fingerprintRegions(seed map[string]string) string {
	entries := make([]string, 0, len(seed))
	for board, region := range seed {
		b := strings.ToLower(strings.TrimSpace(board))
		r := strings.ToLower(strings.TrimSpace(region))
		if b == "" || r == "" {
			continue
		}
		entries = append(entries, b+"="+r)
	}
	sort.Strings(entries)
	h := sha256.Sum256([]byte(strings.Join(entries, "\n")))
	return hex.EncodeToString(h[:])
}

// fingerShort devolve um prefixo legível da impressão digital para o banner (as impressões são
// hex de 64 chars; um prefixo chega para o operador reconhecer a fronteira em vigor).
func fingerShort(f string) string {
	if len(f) > 12 {
		return f[:12]
	}
	return f
}

// itoa/uitoa evitam importar strconv só para dois inteiros de metadados (mantém o ficheiro
// enxuto). Correctos para inteiros não-negativos, que é o único caso aqui (contagens e revisões).
func itoa(n int) string {
	if n < 0 {
		n = 0
	}
	return uitoa(uint64(n))
}

func uitoa(n uint64) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
