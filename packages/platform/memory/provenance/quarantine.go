package provenance

import "github.com/aos-ref/platform/memory/domain"

// Authorization é uma autorização de control-plane para executar uma acção
// privilegiada (ex.: uma tool call). É produzida EXCLUSIVAMENTE por memória
// trusted ([TrustedEntry]); o seu Taint é SEMPRE trusted — não há caminho pelo
// qual memória em quarentena produza uma. É o "token" que o planeador aceita.
//
// O token é SELADO por um campo não exportado (granted) que só este pacote
// consegue pôr a true, via [TrustedEntry.AuthorizeToolCall]. Uma Authorization
// construída directamente por outro pacote (ex.: Authorization{Taint: Trusted})
// tem granted=false: [Authorization.Granted] devolve false e o consumidor a
// jusante distingue um token genuíno de um forjado. Sem este selo, os campos
// exportados tornavam a autoridade de control-plane forjável por construção.
type Authorization struct {
	// Capability é o direito escopado que a memória trusted autoriza exercer.
	Capability string
	// Taint é a proveniência da autorização — invariante: sempre Trusted.
	Taint Provenance
	// granted é o selo de autenticidade: só [TrustedEntry.AuthorizeToolCall] o põe
	// true. Zero-value (forjado directamente) → false.
	granted bool
}

// Granted indica se a autorização é GENUÍNA — emitida por memória trusted via
// [TrustedEntry.AuthorizeToolCall]. Uma Authorization forjada directamente por
// outro pacote devolve false. O consumidor de control-plane DEVE exigir Granted()
// (não basta inspeccionar Taint), pois só o selo não exportado é inforjável.
func (a Authorization) Granted() bool { return a.granted }

// PrivilegedAuthorizer é a CAPACIDADE de autorizar uma acção privilegiada. É o
// coração da barreira estrutural (ADR-005): SÓ memória de control-plane (trusted)
// a satisfaz. Memória em quarentena ([DataItem]) NÃO a implementa — uma asserção
// de tipo falha e a chamada a AuthorizeToolCall nem sequer compila num [DataItem].
//
// O método não exportado isControlPlane veda a interface: só tipos deste pacote a
// podem satisfazer, impedindo que um tipo externo forje pertença ao control-plane.
type PrivilegedAuthorizer interface {
	// AuthorizeToolCall devolve a autorização de control-plane para uma tool call
	// privilegiada. Presente SÓ em memória trusted.
	AuthorizeToolCall(capability string) Authorization
	isControlPlane()
}

// TrustedEntry é uma entrada de memória TRUSTED no caminho do control-plane. É o
// ÚNICO tipo que satisfaz [PrivilegedAuthorizer]: só memória trusted pode
// autorizar uma acção privilegiada. Entra na [TrustedView] que o planeador lê.
//
// O ZERO-VALUE é INERTE por design: o campo não exportado admitted só é posto a
// true por [newTrustedEntry], o construtor guardado que [Partition.Admit] usa após
// encaminhar um [Ingested] SELADO trusted. Um TrustedEntry{} construído
// directamente por outro pacote tem admitted=false e NÃO autoriza nada — a
// inforjabilidade da autoridade passa a ser de TIPO, não de convenção.
type TrustedEntry struct {
	rec      domain.Record
	admitted bool
}

// newTrustedEntry é o ÚNICO construtor de um TrustedEntry utilizável (admitted).
// Só é alcançável por [Partition.Admit], que o chama a partir de um [Ingested]
// selado trusted — nunca a partir de conteúdo ou de uma tag afirmada.
func newTrustedEntry(rec domain.Record) TrustedEntry {
	return TrustedEntry{rec: rec, admitted: true}
}

// Record devolve um clone do registo trusted subjacente.
func (e TrustedEntry) Record() domain.Record { return e.rec.Clone() }

// AuthorizeToolCall implementa [PrivilegedAuthorizer]: produz uma autorização
// trusted GENUÍNA (granted). Só existe em memória trusted — é isto que torna a
// barreira estrutural.
//
// Fail-closed: um zero-value forjado (admitted=false) OU um registo subjacente
// inválido NÃO produz autoridade — devolve uma Authorization não concedida
// (granted=false, sem taint trusted). Assim, nem construir TrustedEntry{}
// directamente nem um registo corrompido conseguem forjar control-plane.
func (e TrustedEntry) AuthorizeToolCall(capability string) Authorization {
	if !e.admitted || e.rec.Validate() != nil {
		return Authorization{}
	}
	return Authorization{Capability: capability, Taint: Trusted, granted: true}
}

func (e TrustedEntry) isControlPlane() {}

// DataItem é memória UNTRUSTED renderizada como DADOS taint-marcados. Expõe o
// conteúdo para o modelo LER (nunca como instruções), e deliberadamente NÃO expõe
// qualquer método que autorize uma acção: NÃO implementa [PrivilegedAuthorizer].
// Uma asserção `any(item).(PrivilegedAuthorizer)` FALHA; `item.AuthorizeToolCall(…)`
// NÃO COMPILA. É a barreira estrutural — a quarentena não comanda acções.
type DataItem struct {
	rec   domain.Record
	taint Provenance
}

// Taint devolve a proveniência do dado (invariante: sempre Untrusted).
func (d DataItem) Taint() Provenance { return d.taint }

// Content devolve um clone do registo untrusted, para o modelo consumir como
// DADOS. Não há aqui qualquer autoridade de acção.
func (d DataItem) Content() domain.Record { return d.rec.Clone() }

// TrustedView é a ÚNICA superfície de memória que o control-plane / planeador
// consome. Por TIPO, só expõe entradas trusted ([TrustedEntry]) — não há método
// que devolva memória untrusted/em quarentena. O planeador não consegue, sequer,
// alcançar a quarentena através deste tipo (segregação de caminho, análoga à
// barreira read-only de AOS-036).
//
// Integração pendente: este tipo MATERIALIZA a propriedade "o planeador só lê
// trusted", mas a LIGAÇÃO a um consumidor real de control-plane (o
// orquestrador/reference-monitor a ler exclusivamente pela TrustedView) é diferida
// para AOS-039. Aqui garante-se o MECANISMO (a impossibilidade estrutural de a
// quarentena autorizar — ver barrier_test); o wiring end-to-end é desse ticket.
type TrustedView struct {
	entries []TrustedEntry
}

// Entries devolve as entradas trusted por ordem de admissão (cópia da slice).
func (v *TrustedView) Entries() []TrustedEntry {
	out := make([]TrustedEntry, len(v.entries))
	copy(out, v.entries)
	return out
}

// Len é o número de entradas trusted.
func (v *TrustedView) Len() int { return len(v.entries) }

// Quarantine detém a memória UNTRUSTED. Serve o conteúdo EXCLUSIVAMENTE como
// [DataItem] (dados taint-marcados) através do [DataPlane]; não tem QUALQUER
// método que produza um control-plane view nem uma autorização. A memória em
// quarentena é estruturalmente incapaz de comandar uma tool call privilegiada.
type Quarantine struct {
	dp      DataPlane
	entries []domain.Record
}

// Items devolve a memória em quarentena renderizada como dados taint-marcados,
// via o [DataPlane]. Cada item é um [DataItem] — nunca um [PrivilegedAuthorizer].
func (q *Quarantine) Items() []DataItem {
	out := make([]DataItem, len(q.entries))
	for i, rec := range q.entries {
		out[i] = q.dp.Serve(rec)
	}
	return out
}

// Len é o número de registos em quarentena.
func (q *Quarantine) Len() int { return len(q.entries) }

// Partition é a memória de um contexto (ex.: um run) já SEPARADA em dois caminhos
// disjuntos: a [TrustedView] (control-plane) e a [Quarantine] (data-plane). A
// admissão ([Admit]) encaminha cada registo pela sua proveniência SELADA — a
// separação é de CAMINHO, imposta na admissão, não uma tag consultada a jusante.
type Partition struct {
	trusted    *TrustedView
	quarantine *Quarantine
}

// NewPartition constrói uma partição vazia. Um dp nil cai no [ReferenceDataPlane]
// (impl de referência do data-plane de EPIC-07).
func NewPartition(dp DataPlane) *Partition {
	if dp == nil {
		dp = ReferenceDataPlane{}
	}
	return &Partition{
		trusted:    &TrustedView{},
		quarantine: &Quarantine{dp: dp},
	}
}

// Admit encaminha um registo admitido pela sua proveniência SELADA:
//
//   - trusted → [TrustedView] (control-plane), como [TrustedEntry] (pode autorizar);
//   - untrusted → [Quarantine] (data-plane), servido depois como [DataItem].
//
// A rota é decidida pelo selo imutável de [Ingested], nunca por conteúdo do
// registo. É este o ponto onde a barreira estrutural é materializada.
func (p *Partition) Admit(in Ingested) {
	if in.prov == Trusted {
		p.trusted.entries = append(p.trusted.entries, newTrustedEntry(in.rec.Clone()))
		return
	}
	p.quarantine.entries = append(p.quarantine.entries, in.rec.Clone())
}

// TrustedView devolve a vista de control-plane (só memória trusted). É o que o
// planeador lê — e o ÚNICO que lê.
func (p *Partition) TrustedView() *TrustedView { return p.trusted }

// Quarantine devolve a quarentena (só memória untrusted, servida como dados).
func (p *Partition) Quarantine() *Quarantine { return p.quarantine }
