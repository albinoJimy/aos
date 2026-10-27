package bancoensaio

import (
	"net/http"
)

// O FORNECEDOR EFECTIVO NO SENTIDO DE VOLTA (AOS-565, primeiro critério: «o proxy reporta o
// fornecedor efectivo de volta?»).
//
// O ADR-043 §2.4 regra 3 exige que «o fornecedor é fixado por pedido … e o fornecedor efectivo
// fica selado». O spike AOS-558 mediu a metade da IDA: `provider`, `modalities`, `plugins` e
// `transforms` chegam ao fornecedor nas duas formas de rota. A metade da VOLTA parte-se em duas,
// e só uma se mede aqui:
//
//   - (i) o fornecedor a montante EMITE a identidade efectiva? Exige fornecedor real e custo.
//     NÃO é desta medição (é o AOS-570a).
//   - (ii) se ela vier no corpo, o proxy DEIXA-A PASSAR até ao cliente? É o que este falso
//     instrumenta.
//
// Com [FalsoDeEstado.FornecedorNaResposta] a resposta de chat na forma da OpenRouter passa a
// levar, ALÉM do que já leva, três sentinelas distintas:
//
//   - a chave de topo `provider` — a forma em que a OpenRouter nomeia quem serviu o pedido;
//   - a chave de topo `aos_sonda` — uma chave INVENTADA, que é o CONTROLO: sem ela a medida não
//     distingue «o proxy preserva chaves de topo desconhecidas em geral» de «o proxy trata a
//     `provider` de forma especial». É o erro que a revisão do AOS-558 corrigiu no instrumento
//     dele (§7 do relatório do spike), e não se repete aqui;
//   - o cabeçalho de resposta [CabecalhoDaSondaDoFornecedor] — porque, se o corpo não passar, um
//     cabeçalho é a via alternativa que o AOS-565 teria de usar, e saber se ELA passa é de graça
//     na mesma corrida.
//
// As três sentinelas têm VALORES DIFERENTES de propósito: com um valor só, uma sentinela que
// aparecesse noutro sítio do corpo não se podia atribuir à chave de onde veio.
//
// MEDE, NÃO JULGA: que o proxy remova qualquer uma delas é um resultado, não um ensaio mal
// montado.
//
// O QUE NÃO PROVA: nada sobre a OpenRouter real. O que este falso responde é escolha de quem o
// escreveu, pelo que um resultado NEGATIVO aqui seria um controlo negativo sobre um recurso
// fabricado — diria que o proxy não deixa passar ESTE corpo, nunca que a OpenRouter não reporta
// o fornecedor. Um resultado POSITIVO, esse, é informativo: o caminho existe.

// As sentinelas que o falso emite com [FalsoDeEstado.FornecedorNaResposta]. Não são segredos: são
// marcas reconhecíveis, e é por elas que se procura no que chegou ao cliente.
const (
	// SentinelaDoFornecedor é o valor da chave de topo `provider` da resposta.
	SentinelaDoFornecedor = "AOS565-FORNECEDOR-DO-FALSO"
	// SentinelaDaSonda é o valor da chave de topo INVENTADA ([ChaveDaSondaDeTopo]) — o controlo.
	SentinelaDaSonda = "AOS565-SONDA-INVENTADA"
	// SentinelaDoCabecalho é o valor do cabeçalho de resposta [CabecalhoDaSondaDoFornecedor].
	SentinelaDoCabecalho = "AOS565-CABECALHO-DO-FALSO"
)

// ChaveDoFornecedorNaResposta é a chave de topo com que a OpenRouter nomeia quem serviu o pedido.
const ChaveDoFornecedorNaResposta = "provider"

// ChaveDaSondaDeTopo é a chave de topo INVENTADA: nenhuma API a define, e é por isso que serve de
// controlo da preservação de chaves de topo desconhecidas.
const ChaveDaSondaDeTopo = "aos_sonda"

// CabecalhoDaSondaDoFornecedor é o cabeçalho de resposta que o falso emite com a sentinela do
// cabeçalho.
const CabecalhoDaSondaDoFornecedor = "X-Aos-Sonda-Fornecedor"

// declararFornecedor põe o cabeçalho da sonda na resposta, quando o falso o emite. Chama-se antes
// de qualquer escrita: depois do primeiro `Write` o cabeçalho já não entra.
func (f *FalsoDeEstado) declararFornecedor(w http.ResponseWriter) {
	if !f.FornecedorNaResposta {
		return
	}
	w.Header().Set(CabecalhoDaSondaDoFornecedor, SentinelaDoCabecalho)
}

// comFornecedor acrescenta as chaves de topo da sonda ao corpo de uma resposta de chat, quando o
// falso as emite. Devolve o mesmo mapa, para se encadear na montagem da resposta.
func (f *FalsoDeEstado) comFornecedor(corpo map[string]any) map[string]any {
	if !f.FornecedorNaResposta {
		return corpo
	}
	corpo[ChaveDoFornecedorNaResposta] = SentinelaDoFornecedor
	corpo[ChaveDaSondaDeTopo] = SentinelaDaSonda
	return corpo
}
