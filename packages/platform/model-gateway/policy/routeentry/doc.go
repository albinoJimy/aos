// Package routeentry é o REGISTO DE ENTRADA ASSINADO de um perfil de rota (AOS-519, emenda ao
// ADR-036 §2.8): a via por que um perfil chega ao nó SEM vir na tabela em código.
//
// # O que é um registo de entrada
//
// Um ficheiro JSON com versão de formato, o perfil da rota (os campos que
// [modelgateway.ParseRouteProfile] lê, e nenhum outro), o digest do perfil, o digest do relatório
// de qualificação que o banco de ensaio produziu para ESSE perfil, o digest da imagem do proxy em
// que a qualificação correu, a fatia inicial do canary, e o board e a região do nó a que se
// destina. Ao lado dele, na mesma pasta: a assinatura ed25519 do dono e o relatório citado.
//
//	<rotulo>.registo.json     o registo de entrada
//	<rotulo>.registo.sig      a assinatura (base64), sobre os bytes EXACTOS do registo
//	<rotulo>.relatorio.json   o relatório de qualificação que o registo cita
//
// # O que a assinatura cobre, e porque chega
//
// A assinatura é sobre os bytes exactos do ficheiro do registo (por via do seu sha256, com um
// prefixo de domínio — [SigningMessage]). O registo traz o digest do relatório, e esse digest é o
// sha256 da forma canónica do relatório inteiro ([ReportDigest]). Logo TUDO o que o nó usa para
// decidir está debaixo da assinatura: o perfil, a fatia, o board e a região directamente; o
// veredicto, o modo da corrida, o perfil e a imagem que o relatório cita, por via do digest.
// Nenhuma decisão se recalcula de um campo que quem entrega os ficheiros possa mudar sem partir a
// assinatura: um relatório trocado tem outro digest, e um registo com outro digest tem outra
// assinatura.
//
// O digest do relatório dá integridade, não autenticidade (AOS-518): quem altera o veredicto pode
// recalcular o digest. É por isso que o nó não confia no digest que o relatório traz de si
// próprio — confere-o E compara-o com o que o registo ASSINADO cita.
//
// # O que a assinatura NÃO prova (limite aceite, ADR-036 §2.8)
//
// Prova que o dono da chave dos perfis assinou AQUELE registo, que cita AQUELE relatório. Não
// prova que o banco de ensaio correu: o relatório é produzido no posto de quem corre e não é
// assinado pelo banco. E o nó não vê que imagem o proxy está de facto a correr: compara o digest
// citado com o declarado no repositório ([ProductionProxyImage]).
//
// # Leitura fechada
//
// O registo e o relatório lêem-se sem casar nomes por caixa e sem aceitar o que não se conhece
// ([strictObject]): um nome de membro repetido, um nome fora do conjunto fechado, um valor de tipo
// errado ou conteúdo depois do documento recusam. A revisão do AOS-518 mostrou porquê: um
// relatório com `"MODO":"real"` acrescentado conferia o digest e um `json.Unmarshal` lia o valor
// forjado.
//
// # Fail-closed
//
// [LoadDir] ou devolve TODOS os registos da pasta conferidos, ou devolve um [*Refusal] com a
// causa em vocabulário fechado ([Causes]) e o rótulo do registo. Não há caminho que carregue «o
// que conseguir». A mensagem nunca repete conteúdo dos ficheiros.
package routeentry
