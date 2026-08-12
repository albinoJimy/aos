package broker

import (
	"context"
	"encoding/json"
)

// PORTA DE AQUISIÇÃO COM CONTEXTO — CONSUMO IN-PROCESS (AOS-265, D8).
//
// O AOS-264 preparou o passo zero (Principal real no ponto de aquisição, cliente
// Vault real, capability da troca). O que FALTAVA era a porta que efectivamente
// CONSOME o broker: uma aquisição que transporta o CONTEXTO DE CHAMADA (Principal +
// run/step, já no [ExchangeRequest]), medeia a troca pelo Reference Monitor e —
// para o consumo v1 IN-PROCESS decidido em D8 — RESOLVE o handle no PROCESSO,
// entregando o material a quem o injecta (o keypool/HTTP do gateway, a sandbox
// server-side), NUNCA ao agente.
//
// # A INVARIANTE RAINHA (desafio A3), preservada e re-declarada
//
// O segredo NUNCA é observável PELO AGENTE. O que o agente obtém da troca é o
// [Handle] OPACO ([Broker.Exchange]); o valor só sai encapsulado num [internal/vault.Secret]
// via [internal/vault.Secret.DeliverTo] para um [GuestSink] SERVER-SIDE. Esta porta
// NÃO relaxa isso: entrega o valor a um sink server-side de CONSUMO IN-PROCESS (o
// [inProcessSink], memória do PRÓPRIO processo, não do agente) e devolve-o embrulhado
// num [ProcessCredential] que se REDIGE em log/JSON/%v. A garantia deixa de ser
// "o pacote nem consegue ler" (verdade no caminho remoto) e passa a ser, no caminho
// in-process, REDACÇÃO no processo — declarado aqui, sem letra pequena. O AGENTE
// continua sem via nenhuma para o valor: o handle que ele vê não resolve nada do
// lado dele.
//
// # FALHA RUÍDOSA ATRIBUÍDA, NUNCA BEARER VAZIO
//
// Uma troca negada por identidade/política devolve o [*DeniedError] da mediação
// (efeito/código/razão — atribuível), tal como [Broker.Exchange]. Uma lease
// expirada/revogada entre a troca e a resolução devolve [ErrLeaseExpired]/[ErrLeaseRevoked];
// material ausente devolve [ErrNoMaterial]. Em NENHUM caso se devolve um
// [ProcessCredential] com valor vazio disfarçado de sucesso — quem consome tem de
// distinguir "não autorizado" de "autenticado sem bearer", senão o A3 volta pela
// porta dos fundos (um turno que corre com credencial vazia contra o upstream).
//
// # DEFERIMENTO DECLARADO — INJECÇÃO NO EXECUTOR REMOTO (D8-B)
//
// Esta porta é o consumo v1 IN-PROCESS. A injecção no executor REMOTO (a microVM
// Firecracker) fica DECLARADAMENTE DEFERIDA (D8-B): aí NÃO se resolve o valor no
// processo do nó — entrega-se o [Handle] OPACO ao orquestrador, que o resolve
// SERVER-SIDE dentro do guest via [Injector.Inject] (o valor nunca atravessa o
// processo do nó nem o do agente). O seam já existe ([Broker.NewInjector] +
// [Injector.Inject], que IGNORA a instância e resolve por posse do handle); o que
// falta é o transporte do handle até ao orquestrador e o binding run↔instância — não
// se constrói aqui. Até lá, o consumo é in-process e o corte downstream real (no
// provedor) exigiria dynamic secrets no Vault (a lease KV v2 corta a INJECÇÃO
// in-process no TTL/revogação, não a credencial no provedor).

// inProcessSink é o [GuestSink] server-side do consumo IN-PROCESS (D8): recebe o
// valor em claro e RETÉM-no para o entregar ao consumidor do PROCESSO (o keypool/
// HTTP do gateway). É a diferença deliberada face ao [MemoryGuest] de referência,
// que DESCARTA o valor — aqui o processo PRECISA do bearer para o apresentar ao
// upstream. Não é código do agente, log, span nem Event Store (os quatro sítios onde
// o segredo nunca pode aparecer): é a fronteira de credencial do processo, e o
// [ProcessCredential] que o embrulha impõe a redacção a jusante.
type inProcessSink struct{ v string }

// Place implementa [GuestSink]: retém o valor server-side para hand-off in-process.
func (c *inProcessSink) Place(_ string, secret string) error {
	c.v = secret
	return nil
}

// ProcessCredential é o material downstream resolvido IN-PROCESS (D8) para consumo
// pelo PROCESSO — nunca pelo agente. O valor é NÃO-EXPORTADO e só sai por [Reveal],
// a ÚNICA egress controlada, chamada na fronteira de credencial do processo (ex.: a
// ponte para o [modelgateway.CredentialProvider], que encapsula logo o segredo numa
// credencial de campo não-exportado). Redigido em [String]/[GoString]/[MarshalJSON]
// (ADR-006): um log/JSON acidental nunca serializa o bearer.
type ProcessCredential struct {
	handle  Handle
	leaseID string
	value   string
}

// Handle devolve o [Handle] OPACO (não-secreto) da lease. É o que se entregaria ao
// orquestrador no caminho REMOTO deferido (D8-B) — aqui expõe-se só para correlação.
func (p ProcessCredential) Handle() Handle { return p.handle }

// LeaseID devolve o id de lease NÃO-SECRETO (correlação/revogação via [Broker.Revoke]).
func (p ProcessCredential) LeaseID() string { return p.leaseID }

// IsZero indica material ausente (nunca deve ser observado num retorno de sucesso:
// [Broker.AcquireInProcess] falha fail-closed em vez de devolver um zero-value).
func (p ProcessCredential) IsZero() bool { return p.value == "" }

// Reveal é a ÚNICA saída do valor, para o consumidor do PROCESSO. Chamada só na
// fronteira de credencial do processo; o valor entra de imediato numa credencial
// redigida a jusante e nunca regressa ao agente.
func (p ProcessCredential) Reveal() string { return p.value }

// String redige o valor (ADR-006): nunca imprime o bearer.
func (p ProcessCredential) String() string {
	return "ProcessCredential{handle=" + string(p.handle) + ",lease=" + p.leaseID + ",value=REDACTED}"
}

// GoString redige o valor também sob %#v (ADR-006).
func (p ProcessCredential) GoString() string { return p.String() }

// MarshalJSON IMPÕE a redacção em JSON: um encoding/json acidental serializa a forma
// redigida, nunca o valor (garantia do TIPO, não da omissão de um campo).
func (p ProcessCredential) MarshalJSON() ([]byte, error) { return json.Marshal(p.String()) }

// AcquireInProcess é a PORTA DE AQUISIÇÃO COM CONTEXTO (AOS-265): medeia a troca pelo
// Reference Monitor ([Broker.Exchange] — transporta Principal/run/step do
// [ExchangeRequest] e SELA o registo por run em [Broker.recordExchange]) e, para o
// consumo v1 IN-PROCESS (D8), RESOLVE o handle no processo, devolvendo o material num
// [ProcessCredential] redigido — para o PROCESSO, nunca para o agente.
//
// Fail-closed e ATRIBUÍDO:
//   - troca negada (identidade/política) ⇒ [*DeniedError] (efeito/código/razão), NUNCA
//     um [ProcessCredential] com valor vazio;
//   - erro de resolução no Vault ([ErrNoMaterial]) ⇒ devolvido tal como;
//   - lease expirada/revogada entre a troca e a resolução ⇒ [ErrLeaseExpired]/
//     [ErrLeaseRevoked] (o mesmo corte de [Lease.injectInto]);
//   - handle sem lease (corrida com o reaper) ⇒ [ErrUnknownHandle].
//
// O AGENTE nunca vê o valor: só o [inProcessSink] server-side o toca, e o
// [ProcessCredential] redige-o a jusante. A injecção no executor REMOTO (D8-B) NÃO
// passa por aqui — ver a nota de deferimento no topo do ficheiro.
func (b *Broker) AcquireInProcess(ctx context.Context, req ExchangeRequest) (ProcessCredential, error) {
	handle, err := b.Exchange(ctx, req)
	if err != nil {
		// Propaga a atribuição da mediação (*DeniedError) ou o erro de resolução —
		// SEM emitir credencial. Nunca bearer vazio.
		return ProcessCredential{}, err
	}
	lease, ok := b.store.get(handle)
	if !ok {
		// A troca emitiu handle mas a lease já não está no store (ex.: reaper
		// concorrente após um TTL nulo). Fail-closed: sem lease, sem material.
		return ProcessCredential{}, ErrUnknownHandle
	}
	var sink inProcessSink
	// injectInto verifica TTL/revogação e entrega SOB o mesmo l.mu (atómico face a
	// [Broker.Revoke]): uma lease já não-injectável NÃO entrega o valor.
	if err := lease.injectInto(&sink, b.clock()); err != nil {
		return ProcessCredential{}, err
	}
	if sink.v == "" {
		// Defesa em profundidade: um Secret zero teria falhado em DeliverTo com
		// ErrNoMaterial; se algum sink devolvesse vazio sem erro, recusamos na mesma —
		// nunca um bearer vazio silencioso.
		return ProcessCredential{}, ErrNoMaterial
	}
	return ProcessCredential{handle: handle, leaseID: lease.ID, value: sink.v}, nil
}
