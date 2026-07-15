// Package adapters define a INTERFACE de adaptador de provider do Model Gateway
// e implementações de referência (AOS-055, tecnica/06 §3). Os adaptadores são
// DETALHE de implementação atrás da porta [port.Gateway]: traduzem a forma
// normalizada compatível OpenAI para o wire concreto de cada provedor e de volta.
//
// Fornece dois adaptadores que satisfazem a MESMA interface sem alterar o
// contrato de porta:
//   - [OpenAIHTTPAdapter] — adaptador REAL, HTTP OpenAI-compatible (net/http +
//     wire format OpenAI), estruturalmente completo e testável contra httptest.
//   - [FakeAdapter] — adaptador in-memory DETERMINISTA para testes.
//
// # Segredos (ADR-006)
//
// As credenciais NUNCA são hard-coded nem passadas por [port.ChatRequest]. Entram
// por uma porta [CredentialSource] e são transportadas em [Credential], cujo
// segredo é NÃO-EXPORTADO — nenhum pacote fora de adapters o consegue ler, e o
// [Credential.String]/redação garante que não aparece em logs/spans.
package adapters

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/aos-ref/platform/model-gateway/port"
)

// ErrNoCredential — o [CredentialSource] não tem credencial para o par
// (provider, região) pedido. É fail-closed: sem credencial válida a chamada é
// recusada, nunca cai para outra conta/região silenciosamente (cruza AOS-056).
var ErrNoCredential = errors.New("adapters: sem credencial para provider/regiao")

// ErrEmptyCredential — a credencial resolvida não tem segredo. O adaptador
// RECUSA-a (fail-closed) em vez de enviar um pedido NÃO-autenticado ao provedor —
// um segredo vazio nunca é tratado como "sem auth necessária" (ADR-006).
var ErrEmptyCredential = errors.New("adapters: credencial sem segredo (fail-closed)")

// Credential é a credencial de infra de um provider, obtida via
// [CredentialSource] server-side. O segredo é NÃO-EXPORTADO: o agente e os
// planos consumidores nunca o vêem (ADR-006). Só o adaptador, dentro deste
// pacote, o lê via [Credential.secretValue].
type Credential struct {
	Provider string
	Region   string
	secret   string
}

// NewCredential constrói uma [Credential]. O segredo fica encapsulado; não há
// getter exportado.
func NewCredential(provider, region, secret string) Credential {
	return Credential{Provider: provider, Region: region, secret: secret}
}

// secretValue devolve o segredo (uso interno do adaptador real, no mesmo pacote).
func (c Credential) secretValue() string { return c.secret }

// String redige o segredo — garante que uma Credential impressa em log/erro
// nunca revela a chave (ADR-006).
func (c Credential) String() string {
	return "Credential{provider=" + c.Provider + ",region=" + c.Region + ",secret=REDACTED}"
}

// MarshalJSON IMPÕE a redação também em JSON (ADR-006): um encoding/json acidental
// (log/span estruturado) serializa a forma redigida, nunca o segredo. A garantia
// é imposta pelo tipo, não apenas pela omissão do campo não-exportado.
func (c Credential) MarshalJSON() ([]byte, error) {
	return json.Marshal(c.String())
}

// KeyID devolve um identificador NÃO-SECRETO e estável da credencial — um
// fingerprint DETERMINISTA do segredo (SHA-256 truncado a 12 hex) para
// correlação/atribuição em observabilidade e testes SEM revelar o segredo. É
// unidireccional: não permite recuperar a chave (ADR-006). Uma credencial sem
// segredo devolve "". Usado, por exemplo, para provar que uma rotação de chave
// produziu uma credencial DIFERENTE sem comparar segredos em claro.
func (c Credential) KeyID() string {
	if c.secret == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(c.secret))
	return hex.EncodeToString(sum[:6])
}

// CredentialSource é a PORTA de aquisição de credenciais de infra (ADR-006). A
// implementação real (broker/vault JIT) é AOS-056; aqui os adaptadores consomem
// a porta e os testes fornecem uma fonte fake.
type CredentialSource interface {
	// Fetch obtém a credencial para o par (provider, região). Fail-closed:
	// devolve [ErrNoCredential] se não houver credencial válida.
	Fetch(ctx context.Context, provider, region string) (Credential, error)
}

// Adapter é a interface de adaptador de provider. Recebe a forma NORMALIZADA da
// porta e a [Credential] injectada server-side; devolve a forma normalizada.
// Todos os adaptadores (real e fake) implementam-na SEM alterar o contrato de
// [port.Gateway].
type Adapter interface {
	// Provider devolve o identificador estável do provedor (ex.: "openai").
	Provider() string
	// Chat executa uma chamada de chat síncrona contra o provedor.
	Chat(ctx context.Context, req port.ChatRequest, cred Credential) (port.ChatResponse, error)
	// ChatStream executa uma chamada de chat em streaming.
	ChatStream(ctx context.Context, req port.ChatRequest, cred Credential) (port.ChatStream, error)
	// Embeddings executa uma chamada de embeddings.
	Embeddings(ctx context.Context, req port.EmbeddingsRequest, cred Credential) (port.EmbeddingsResponse, error)
}

// StaticCredentialSource é uma [CredentialSource] in-memory determinista para
// testes: um mapa (provider|região) → segredo. NUNCA usar em produção (a fonte
// real é o broker/vault JIT de AOS-056).
type StaticCredentialSource struct {
	// creds mapeia a chave "provider|regiao" para o segredo.
	creds map[string]string
}

// NewStaticCredentialSource constrói uma fonte estática vazia.
func NewStaticCredentialSource() *StaticCredentialSource {
	return &StaticCredentialSource{creds: map[string]string{}}
}

// Set regista um segredo para o par (provider, região).
func (s *StaticCredentialSource) Set(provider, region, secret string) {
	s.creds[provider+"|"+region] = secret
}

// Fetch implementa [CredentialSource] fail-closed. Um segredo registado VAZIO é
// tratado como ausência de credencial ([ErrNoCredential]) — nunca se devolve uma
// credencial sem segredo que produziria um pedido não-autenticado a jusante.
func (s *StaticCredentialSource) Fetch(_ context.Context, provider, region string) (Credential, error) {
	secret, ok := s.creds[provider+"|"+region]
	if !ok || secret == "" {
		return Credential{}, ErrNoCredential
	}
	return NewCredential(provider, region, secret), nil
}
