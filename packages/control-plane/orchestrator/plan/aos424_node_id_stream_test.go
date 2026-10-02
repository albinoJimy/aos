package plan

// aos424_node_id_stream_test.go — O CHARSET DO `node_id` ESTÁ ACOPLADO AO ESPAÇO DE NOMES DO
// EVENT STORE, E QUEM O MEXER TEM DE SABER.
//
// Este teste não existe para defender o charset: existe para que quem o APERTAR veja o
// acoplamento no sítio onde está a editar.
//
// # O ACOPLAMENTO
//
// O `ValidNodeID` admite `.` e `:`. O executor de nós do `aos-orq` compõe o id do run filho
// como `<run>~<node_id>` (`childRunID`, `cmd/aos-orq/node_executor.go`) e submete-o ao nó por
// `POST /runs`. E o `run_id` de um run **é** o seu stream no Event Store.
//
// Consequência: um nó de plano chamado `analise.dados` produz o stream
// `run-x~analise.dados` — que o backend JetStream **recusa**, porque um subject NATS não
// representa o ponto (`jetstream.Store.subjectDe`). Sobre o substrato de ficheiro funciona.
//
// # PORQUE É QUE ISTO NÃO FOI RESOLVIDO A APERTAR O CHARSET
//
// Porque apertá-lo é uma mudança SEMÂNTICA que afecta o que o planeador pode produzir: um
// plano com um `node_id` que hoje é válido passaria a ser recusado, e o prompt de decomposição
// tem de o saber. O AOS-424 registou-o como decisão do dono em vez de a tomar de passagem — e,
// enquanto ela não for tomada, o `POST /runs` NÃO valida o `run_id`, porque validá-lo partiria
// os planos cujos nós usem esses caracteres.
//
// # SE APERTAR O CHARSET
//
// Este teste fica vermelho, e é esse o objectivo. Ao actualizá-lo, **ligue também**
// `runIDInvalido(req.RunID)` ao `handleSubmit` em `packages/cmd/aos/api.go`: a razão que
// bloqueava essa guarda deixou de existir. O `TestAOS424PostRunsAindaNaoValidaEPorque`, do
// lado do nó, lê este ficheiro e também fica vermelho — mas é aqui que a mudança começa.

import "testing"

// TestAOS424CharsetDoNodeIDEstaAcopladoAoStream fixa os caracteres que a grammar admite E que
// NÃO são representáveis num subject NATS.
//
// A asserção é deliberadamente estreita: só `.` e `:`. Os outros caracteres do charset
// (letras, dígitos, `_`, `-`) são representáveis e não interessam a este eixo.
func TestAOS424CharsetDoNodeIDEstaAcopladoAoStream(t *testing.T) {
	// Os dois caracteres admitidos que um subject NATS não representa.
	for _, id := range []string{"analise.dados", "etapa:build"} {
		if !ValidNodeID(id) {
			t.Errorf("o `node_id` %q deixou de ser aceite pela grammar.\n"+
				"Se foi DELIBERADO (apertar o charset para o tornar stream-safe): ligue também "+
				"`runIDInvalido(req.RunID)` ao `handleSubmit` em packages/cmd/aos/api.go — a razão "+
				"que bloqueava essa guarda desapareceu. Ver o cabeçalho deste ficheiro e AOS-424.", id)
		}
	}

	// CONTROLO DE NÃO-VACUIDADE: a grammar continua a ser uma grammar, e não um «aceita tudo».
	// Sem isto, um `ValidNodeID` que devolvesse sempre `true` passaria no bloco acima.
	for _, id := range []string{"", "nó com espaço", "barra/dentro", "til~dentro", "cardinal#"} {
		if ValidNodeID(id) {
			t.Errorf("o `node_id` %q foi ACEITE: a grammar deixou de restringir, e o bloco acima "+
				"passa a medir nada", id)
		}
	}
}
