package revalidation

import (
	"context"
	"crypto/ed25519"

	"github.com/aos-ref/platform/registry/domain"
)

// TrustStore é a PORTA mínima que a revalidação consome do pilar assinatura
// (AOS-048): a consulta da chave PÚBLICA de um publicador confiável. *signing.
// TrustStore satisfá-la via Lookup. Mantê-la mínima desacopla a revalidação da
// superfície completa do trust store e permite fakes deterministas em teste. O
// revalidador NUNCA toca uma chave privada nem guarda segredos.
type TrustStore interface {
	// Lookup devolve a chave pública do keyID e um booleano de presença. Uma chave
	// ausente OU revogada devolve (_, false) — fail-closed.
	Lookup(keyID string) (ed25519.PublicKey, bool)
}

// EgressAllowlist é a PORTA de egress por HOST (EPIC-07, AOS-046/067): decide se o
// host concreto alvo de uma chamada é permitido. Fail-closed: um host ausente é
// NEGADO. mcp.StaticEgressAllowlist satisfá-la estruturalmente. É OPCIONAL: se nil,
// só a CLASSE de egress (none/internal/external) é verificada contra o tecto da
// política; havendo allowlist e host, verifica-se também o host.
type EgressAllowlist interface {
	// Allowed indica se o egress para o host (sem porta) é permitido.
	Allowed(host string) bool
}

// Artifact é a identidade pública de um artefacto divergente encaminhado para
// quarentena/alerta. NUNCA contém segredos nem o payload da tool — apenas a
// identidade pinada (id, version, digest) e a razão do isolamento.
type Artifact struct {
	// ID, Version, Digest identificam o artefacto divergente.
	ID      string
	Version string
	Digest  string
	// Reason é o código estável do drift/violação que motivou o isolamento.
	Reason Reason
}

// Quarantiner é a PORTA de QUARENTENA (AOS-042): isola um artefacto divergente para
// que não volte a ser resolvido/executado enquanto o incidente não for tratado. É a
// costura para a máquina de quarentena real — o mesmo padrão de porta usado no resto
// do REG; produção liga-a a AOS-042, os testes a um recorder. A revalidação NÃO
// reimplementa a quarentena; compõe-na. O isolamento é BEST-EFFORT do ponto de vista
// do bloqueio: uma falha de quarentena NÃO desbloqueia a chamada (o bloqueio já é
// fail-closed), mas é reportada ao alerter.
type Quarantiner interface {
	// Quarantine isola o artefacto divergente. Um erro é reportado, nunca ignora o
	// bloqueio.
	Quarantine(ctx context.Context, art Artifact) error
}

// Alert é o evento de ALERTA emitido numa divergência (drift/assinatura/scope/
// egress). É público (id/version/digest/stage/reason) — nunca scopes nem segredos.
type Alert struct {
	// ToolID, Version, Digest identificam o artefacto divergente.
	ToolID  string
	Version string
	Digest  string
	// Stage é o passo fail-closed que detectou a divergência.
	Stage Stage
	// Reason é o código estável da divergência.
	Reason Reason
	// QuarantineErr é o erro (se algum) da tentativa de quarentena — para que o
	// alerta reflicta uma quarentena que falhou (o incidente agrava-se).
	QuarantineErr error
}

// Alerter é a PORTA de ALERTA: emite um incidente de segurança quando uma tool call
// é bloqueada por divergência. Costura para o pipeline de alertas real (EPIC-08);
// produção liga-a, os testes a um recorder. O alerta é BEST-EFFORT — nunca
// desbloqueia nem bloqueia; é observacional/reactivo.
type Alerter interface {
	Alert(ctx context.Context, alert Alert)
}

// --- Impls de referência (defaults seguros) --------------------------------

// NoopQuarantiner é a quarentena por omissão: não isola nada. É um NO-OP
// DELIBERADO para o revalidador ser exercitável sem uma máquina de quarentena
// ligada; PRODUÇÃO DEVE injectar [WithQuarantiner] com o mecanismo de AOS-042, senão
// os artefactos divergentes ficam bloqueados mas não isolados.
type NoopQuarantiner struct{}

// Quarantine implementa [Quarantiner] — não faz nada, nunca falha.
func (NoopQuarantiner) Quarantine(context.Context, Artifact) error { return nil }

// NoopAlerter é o alerter por omissão: descarta os alertas. Produção injecta
// [WithAlerter] com o pipeline real.
type NoopAlerter struct{}

// Alert implementa [Alerter] — descarta.
func (NoopAlerter) Alert(context.Context, Alert) {}

// Policy é a política de scopes/egress do RUN contra a qual o passo (4) verifica o
// contract. É a fronteira "dentro do permitido" (ADR-006 + EPIC-07): os scopes de
// credencial declarados têm de ser um SUBCONJUNTO dos permitidos e a classe de
// egress não pode exceder o tecto. O zero-value é o MAIS restritivo (nenhum scope
// permitido, egress máximo "none") — fail-closed por omissão.
type Policy struct {
	// AllowedScopes são os scopes de credencial que o run permite conceder. Um
	// contract que declare um scope fora deste conjunto é bloqueado. Vazio = nenhum
	// scope permitido (uma tool sem scopes passa; uma tool com scopes é bloqueada).
	AllowedScopes []string
	// MaxEgress é o TECTO da classe de egress permitida no run. Uma classe de rank
	// superior (none < internal < external) é bloqueada. Zero-value ("") é tratado
	// como "none" — o tecto mais restritivo.
	MaxEgress domain.EgressClass
}

// egressRank ordena as classes de egress por risco crescente: none < internal <
// external. Uma classe desconhecida/vazia cai no rank 0 (none) — fail-closed no
// tecto (nada acima de none é permitido por omissão) e conservador na declaração
// (uma classe malformada nunca é tratada como MAIS permissiva do que é).
func egressRank(e domain.EgressClass) int {
	switch e {
	case domain.EgressExternal:
		return 2
	case domain.EgressInternal:
		return 1
	default: // EgressNone e qualquer valor não canónico
		return 0
	}
}
