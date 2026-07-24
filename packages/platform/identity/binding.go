package identity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/aos-ref/substrate/eventstore"
)

// Rótulos de método/autoridade de autorização gravados no registo de binding
// (campo AuthMethod do evento identity.nhi.issued). São RÓTULOS estáveis — nunca
// o token/asserção cru nem PII. As impls de autoridade (ex.: integration.OIDCDirectory,
// integration.AllowlistDirectory) fornecem o rótulo concreto; estes cobrem os casos
// genéricos deste pacote.
const (
	// AuthMethodUnspecified é o método por omissão quando o mint não declara um
	// contexto de autorização. Torna o binding honesto: o registo diz explicitamente
	// que o método não foi declarado, em vez de o omitir.
	AuthMethodUnspecified = "unspecified"
	// AuthMethodDelegation marca uma NHI FILHA autorizada pela cadeia de delegação
	// do pai (on-behalf-of). A autoria resolve na mesma raiz humana do pai.
	AuthMethodDelegation = "delegation"
)

// ErrBindingNotFound — nenhuma emissão (identity.nhi.issued) no stream de identidade
// corresponde ao critério de auditoria (jti/agente). Fail-closed: a ausência de um
// binding registado é sempre negação, nunca uma atribuição inventada.
var ErrBindingNotFound = &IdentityError{Code: "E_BINDING_NOT_FOUND", msg: "nenhum binding humano<->NHI registado corresponde ao criterio"}

// BindingRecord é o REGISTO AUDITÁVEL do binding humano↔NHI, reconstruído a partir
// do log append-only de identidade (ADR-003). Responde, para uma NHI, "quem
// autorizou" (o humano na raiz da cadeia), "quando" e "por que método/autoridade".
//
// Sem segredos/PII: só identificadores e metadados. Human é o principal da raiz da
// cadeia de delegação ("human:<id>"), a FONTE-DE-VERDADE de autoria (não o campo de
// conveniência do payload) — resolve fail-closed via [AuthorFromEventChain], logo
// nunca atribui autoria a um principal não-humano ("pool").
type BindingRecord struct {
	// Human é o humano responsável na raiz da cadeia de delegação ("human:<id>").
	Human string
	// AgentID é a NHI (o agente) a que o humano se liga.
	AgentID string
	// AgentClass é a classe sob cuja política a NHI foi emitida.
	AgentClass string
	// Scope são as capabilities concedidas à NHI no mint.
	Scope []string
	// JTI identifica univocamente a emissão (chave de idempotência do binding).
	JTI string
	// Issuer é o emissor (iss) que selou o binding.
	Issuer string
	// AuthMethod é o contexto de autorização: método/autoridade pelo qual o humano
	// foi autenticado (ex.: "oidc:<issuer>", "allowlist", "delegation").
	AuthMethod string
	// IssuedAt e Expiry são o instante do binding e o prazo da NHI (segundos Unix).
	IssuedAt int64
	Expiry   int64
}

// bindingReader é o subconjunto do Event Store de que a auditoria de binding
// depende (só leitura append-only). Minimizá-lo desacopla o auditor da superfície
// completa do store e facilita testes.
type bindingReader interface {
	Read(ctx context.Context, streamID string, fromSeq uint64) ([]eventstore.Event, error)
}

// BindingAudit é a PORTA de auditoria/resolução do binding humano↔NHI. Lê o stream
// append-only de identidade e resolve, para uma NHI (por jti ou por agente), o
// registo auditável de binding — sem nunca reescrever o log. É a materialização,
// consultável, da invariante do ADR-003/§13.2: toda a NHI resolve para um humano
// responsável; zero acções "pool".
type BindingAudit struct {
	store bindingReader
}

// NewBindingAudit constrói o auditor sobre um Event Store (ou qualquer leitor
// append-only compatível). Um store nil produz um auditor que falha fail-closed em
// cada consulta ([ErrBindingNotFound]).
func NewBindingAudit(store bindingReader) *BindingAudit {
	return &BindingAudit{store: store}
}

// ResolveByJTI resolve o binding da emissão identificada por jti: devolve "quem
// autorizou" (humano na raiz) + quando + por que método. Fail-closed:
//
//   - jti vazio ou store nil ⇒ [ErrBindingNotFound];
//   - nenhuma emissão com esse jti ⇒ [ErrBindingNotFound];
//   - a emissão existe mas a sua cadeia é ÓRFÃ (raiz não-humana) ou vazia ⇒
//     [delegation.ErrOrphanChain]/[delegation.ErrEmptyChain] — NUNCA se devolve um
//     binding que atribua a NHI a um principal não-humano.
func (b *BindingAudit) ResolveByJTI(ctx context.Context, jti string) (BindingRecord, error) {
	if b == nil || b.store == nil || jti == "" {
		return BindingRecord{}, ErrBindingNotFound
	}
	events, err := b.readIdentity(ctx)
	if err != nil {
		return BindingRecord{}, err
	}
	for _, ev := range events {
		if ev.Type != EventTypeIssued {
			continue
		}
		pl, perr := decodeIssued(ev)
		if perr != nil {
			// Um registo malformado ISOLADO não pode abortar a auditoria inteira
			// (um só evento corrupto seria um DoS de auditoria sobre todos os
			// bindings): salta-se. Um payload ilegível nunca é um binding resolvel
			// — a garantia contra adulteração é o append-only/WORM do log (ADR-010),
			// não a paragem da varredura. Fail-closed mantém-se: um alvo cujo próprio
			// registo esteja corrupto resolve para ErrBindingNotFound (nenhum binding),
			// nunca para uma atribuição inventada.
			continue
		}
		if pl.JTI != jti {
			continue
		}
		return bindingFromEvent(ev, pl)
	}
	return BindingRecord{}, ErrBindingNotFound
}

// BindingsForAgent resolve TODOS os bindings de um agente (NHI), por ordem de
// emissão (seq ascendente). Uma NHI re-emitida (rotação de token) tem vários
// bindings, todos enraizados no mesmo humano. Fail-closed: agente sem qualquer
// binding ⇒ [ErrBindingNotFound]; uma emissão com cadeia órfã aborta a resolução
// com o erro de delegação (nunca devolve um binding não-humano).
func (b *BindingAudit) BindingsForAgent(ctx context.Context, agentID string) ([]BindingRecord, error) {
	if b == nil || b.store == nil || agentID == "" {
		return nil, ErrBindingNotFound
	}
	events, err := b.readIdentity(ctx)
	if err != nil {
		return nil, err
	}
	var out []BindingRecord
	for _, ev := range events {
		if ev.Type != EventTypeIssued {
			continue
		}
		pl, perr := decodeIssued(ev)
		if perr != nil {
			// Salta um registo malformado isolado (ver ResolveByJTI): um evento
			// corrupto não bloqueia a resolução dos bindings válidos do agente.
			continue
		}
		if pl.AgentID != agentID {
			continue
		}
		rec, rerr := bindingFromEvent(ev, pl)
		if rerr != nil {
			return nil, rerr
		}
		out = append(out, rec)
	}
	if len(out) == 0 {
		return nil, ErrBindingNotFound
	}
	return out, nil
}

// readIdentity lê o stream append-only de identidade completo. Um stream ainda
// inexistente (nenhuma emissão jamais gravada) é mapeado fail-closed para
// [ErrBindingNotFound] — "sem binding registado" é uma negação limpa, não um erro de
// I/O que o chamador tenha de distinguir.
func (b *BindingAudit) readIdentity(ctx context.Context) ([]eventstore.Event, error) {
	events, err := b.store.Read(ctx, streamIdentity, 1)
	if err != nil {
		if errors.Is(err, eventstore.ErrStreamNotFound) {
			return nil, ErrBindingNotFound
		}
		return nil, err
	}
	return events, nil
}

// decodeIssued descodifica o payload de um evento identity.nhi.issued. Um payload
// malformado devolve erro (envolvendo [ErrBindingNotFound]); os chamadores da
// varredura tratam-no como um registo isolado a SALTAR — um evento corrupto nunca é
// um binding resolvel e não pode abortar a auditoria dos restantes (ver ResolveByJTI).
func decodeIssued(ev eventstore.Event) (issuedPayload, error) {
	var pl issuedPayload
	if err := json.Unmarshal(ev.Payload, &pl); err != nil {
		return issuedPayload{}, fmt.Errorf("%w: payload de emissao ilegivel: %w", ErrBindingNotFound, err)
	}
	return pl, nil
}

// bindingFromEvent projecta um evento de emissão para um [BindingRecord]. O humano
// responsável é derivado da CADEIA de delegação do produtor (não do campo de
// conveniência user_id): [AuthorFromEventChain] impõe raiz humana fail-closed, logo
// uma cadeia órfã/vazia devolve o erro de delegação e NENHUM binding — a auditoria
// nunca legitima uma NHI "pool".
func bindingFromEvent(ev eventstore.Event, pl issuedPayload) (BindingRecord, error) {
	human, err := AuthorFromEventChain(ev.Producer.DelegationChain)
	if err != nil {
		return BindingRecord{}, err
	}
	method := pl.AuthMethod
	if method == "" {
		method = AuthMethodUnspecified
	}
	return BindingRecord{
		Human:      human,
		AgentID:    pl.AgentID,
		AgentClass: pl.AgentClass,
		Scope:      pl.Scope,
		JTI:        pl.JTI,
		Issuer:     pl.Issuer,
		AuthMethod: method,
		IssuedAt:   pl.IssuedAt,
		Expiry:     pl.Expiry,
	}, nil
}
