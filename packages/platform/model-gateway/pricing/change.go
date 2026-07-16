package pricing

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"time"

	"github.com/aos-ref/platform/audit"
)

// Este ficheiro torna uma ALTERAÇÃO DE PREÇO um EVENTO EXPLÍCITO (AOS-062, ADR-011):
// a diferença entre duas versões da tabela é computada ([Diff]), emitida por um sink
// e selada no changelog WORM tamper-evident ([ChangeRecorder]). Uma mudança de preço
// nunca é silenciosa — o digest muda (versão nova) E a activação é auditável, para
// que o burn-down e a reconciliação nunca sejam falsificados por uma alteração
// escondida.

// ErrEmptyVersion — tentativa de selar/emitir uma alteração com versão vazia.
var ErrEmptyVersion = errors.New("pricing: versao de tabela vazia (fail-closed)")

// ChangeKind classifica a alteração de UMA chave (modelo, região) entre versões.
type ChangeKind string

const (
	// ChangeAdded — o (modelo, região) passou a ter preço (não existia antes).
	ChangeAdded ChangeKind = "added"
	// ChangeRemoved — o (modelo, região) deixou de ter preço (existia antes).
	ChangeRemoved ChangeKind = "removed"
	// ChangeUpdated — o (modelo, região) existia e algum rate mudou.
	ChangeUpdated ChangeKind = "updated"
)

// RateChange descreve a alteração de preço de UMA chave (modelo, região): o tipo e,
// para uma actualização, os rates antigo→novo (para o registo/reconciliação).
type RateChange struct {
	Key  Key
	Kind ChangeKind
	Old  Rate
	New  Rate
}

// ChangeEvent é o EVENTO EXPLÍCITO de alteração de preços entre duas versões da
// tabela: a versão de origem, a de destino e a lista ORDENADA de alterações por
// chave. É o dado emitido/selado — a prova de que a mudança não foi silenciosa.
type ChangeEvent struct {
	FromVersion string
	ToVersion   string
	Changes     []RateChange
	At          time.Time
}

// Empty indica que não houve qualquer alteração de preço (as duas versões têm os
// mesmos rates). Um evento vazio corresponde a digests iguais.
func (e ChangeEvent) Empty() bool { return len(e.Changes) == 0 }

// Diff computa o EVENTO de alteração entre duas tabelas (old→new). Detecta chaves
// adicionadas, removidas e actualizadas (rate alterado), por ordem determinista de
// (modelo, região). Uma tabela old nil é tratada como vazia (tudo "added") — o caso
// da activação inicial. É uma função PURA (sem relógio nem I/O); o timestamp é
// atribuído por quem sela o evento.
func Diff(old, new *Table) ChangeEvent {
	ev := ChangeEvent{ToVersion: versionOf(new)}
	if old != nil {
		ev.FromVersion = old.Version()
	}
	oldByKey := map[Key]Rate{}
	if old != nil {
		oldByKey = old.byKey
	}
	newByKey := map[Key]Rate{}
	if new != nil {
		newByKey = new.byKey
	}
	seen := map[Key]struct{}{}
	for k, nr := range newByKey {
		seen[k] = struct{}{}
		or, existed := oldByKey[k]
		switch {
		case !existed:
			ev.Changes = append(ev.Changes, RateChange{Key: k, Kind: ChangeAdded, New: nr})
		case or != nr:
			ev.Changes = append(ev.Changes, RateChange{Key: k, Kind: ChangeUpdated, Old: or, New: nr})
		}
	}
	for k, or := range oldByKey {
		if _, ok := seen[k]; !ok {
			ev.Changes = append(ev.Changes, RateChange{Key: k, Kind: ChangeRemoved, Old: or})
		}
	}
	sort.Slice(ev.Changes, func(i, j int) bool {
		a, b := ev.Changes[i].Key, ev.Changes[j].Key
		if a.Model != b.Model {
			return a.Model < b.Model
		}
		return a.Region < b.Region
	})
	return ev
}

func versionOf(t *Table) string {
	if t == nil {
		return ""
	}
	return t.Version()
}

// ChangeSink recebe, opcionalmente, cada [ChangeEvent] emitido (introspecção em
// memória; produção liga o barramento de eventos). A selagem WORM é separada.
type ChangeSink interface {
	Emit(ctx context.Context, ev ChangeEvent)
}

// ChangeSinkFunc adapta uma função a [ChangeSink].
type ChangeSinkFunc func(ctx context.Context, ev ChangeEvent)

// Emit implementa [ChangeSink].
func (f ChangeSinkFunc) Emit(ctx context.Context, ev ChangeEvent) { f(ctx, ev) }

// changelogPartition é a cadeia dedicada ao CHANGELOG de preços (ADR-011): cada
// activação de uma versão da tabela sela aqui, tornando a evolução dos preços
// tamper-evident e auditável independentemente dos registos de custo por chamada.
const changelogPartition = "modelgw-cost:pricing-changelog"

// capabilityActivate é a capability selada na activação de uma versão de preços.
const capabilityActivate = "pricing:activate"

// obligationPriceChange é o tipo da obligation que sela o resumo da alteração
// (versões e nº de mudanças) na cadeia tamper-evident.
const obligationPriceChange = "price_change"

// ChangeRecorder sela e emite a ACTIVAÇÃO/ALTERAÇÃO de uma versão da tabela de
// preços — o EVENTO EXPLÍCITO exigido por AOS-062/ADR-011. Construir com
// [NewChangeRecorder].
type ChangeRecorder struct {
	store audit.Store
	sink  ChangeSink
}

// ChangeOption configura o [ChangeRecorder].
type ChangeOption func(*ChangeRecorder)

// WithChangeSink liga um [ChangeSink] em memória que recebe cada evento (além da
// selagem WORM). Útil para observabilidade e testes.
func WithChangeSink(s ChangeSink) ChangeOption {
	return func(r *ChangeRecorder) {
		if s != nil {
			r.sink = s
		}
	}
}

// NewChangeRecorder constrói o recorder sobre um audit [audit.Store] (WORM). Sem
// store, a selagem é no-op (só a emissão para o sink corre) — produção liga um store
// real.
func NewChangeRecorder(store audit.Store, opts ...ChangeOption) *ChangeRecorder {
	r := &ChangeRecorder{store: store}
	for _, o := range opts {
		o(r)
	}
	return r
}

// Activate REGISTA a activação de uma versão de preços a partir de uma alteração
// (old→new): emite o [ChangeEvent] para o sink e SELA-o no changelog WORM. É o
// ponto único que torna a alteração explícita. Chamado no arranque (old nil) e em
// cada rotação de preços. Fail-closed: uma versão de destino vazia é recusada.
//
// Um evento VAZIO (sem alterações de rate) NÃO é selado nem emitido — reactivar a
// mesma tabela não polui o changelog com ruído; só uma alteração REAL produz evento.
func (r *ChangeRecorder) Activate(ctx context.Context, old, new *Table, at time.Time) (ChangeEvent, error) {
	if new == nil || new.VersionTag == "" {
		return ChangeEvent{}, ErrEmptyVersion
	}
	ev := Diff(old, new)
	ev.At = at
	if ev.Empty() {
		return ev, nil
	}
	if r.sink != nil {
		r.sink.Emit(ctx, ev)
	}
	if err := r.seal(ctx, ev); err != nil {
		return ChangeEvent{}, err
	}
	return ev, nil
}

// seal grava o evento no changelog WORM (se houver store). Cada RateChange sela
// como uma obligation individual (chave + tipo) para a alteração ser reconstruível
// a partir de UM registo, não só do resumo.
func (r *ChangeRecorder) seal(ctx context.Context, ev ChangeEvent) error {
	if r.store == nil {
		return nil
	}
	obligations := []audit.Obligation{{
		Type: obligationPriceChange,
		Params: map[string]string{
			"from_version": ev.FromVersion,
			"to_version":   ev.ToVersion,
			"num_changes":  strconv.Itoa(len(ev.Changes)),
		},
	}}
	for _, c := range ev.Changes {
		obligations = append(obligations, audit.Obligation{
			Type: obligationPriceChange,
			Params: map[string]string{
				"model":  c.Key.Model,
				"region": c.Key.Region,
				"kind":   string(c.Kind),
			},
		})
	}
	ar := audit.AuditRecord{
		Partition:     changelogPartition,
		Timestamp:     ev.At,
		Decision:      audit.DecisionAllow,
		Principal:     audit.Principal{NHIID: "pricing-table"},
		Capability:    capabilityActivate,
		PolicyVersion: ev.ToVersion,
		ToolID:        "pricing.table",
		Resource:      audit.Resource{Type: "pricing", Value: "model-region-table"},
		Obligations:   obligations,
	}
	_, err := r.store.Append(ctx, ar)
	return err
}
