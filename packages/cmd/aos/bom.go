package main

import "bytes"

// ---------------------------------------------------------------------------------------------
// O BOM NÃO PODE DERRUBAR O ARRANQUE POR UM DIAGNÓSTICO ILEGÍVEL.
//
// COMO APARECEU. Corri o selador duas vezes seguidas (`selar-worm.ps1`) e a SEGUNDA não conseguiu
// ler o ficheiro de checkpoints que a PRIMEIRA tinha escrito:
//
//	--anterior nao e JSON de checkpoints: invalid character 'ï' looking for beginning of value
//
// O `Set-Content -Encoding utf8` do PowerShell 5.1 escreve um BOM UTF-8, e o `encoding/json` do
// Go recusa-o. O `ï` é o primeiro byte do BOM lido como Latin-1.
//
// PORQUE IMPORTA AQUI E NÃO SÓ NO SELADOR. O nó lê os MESMOS ficheiros. Um operador que sele em
// Windows — o caso normal, porque é onde as chaves do operador vivem — montaria um ficheiro com
// BOM e o nó abortaria em [ErrBadWormCheckpoint]. A postura estaria certa (fail-closed, e é o que
// se quer), mas o diagnóstico é um byte inválido: ninguém o liga a codificação, e a verificação
// ancorada ficaria por ligar com a conclusão errada de que «o ficheiro está corrompido».
//
// A NORMALIZAÇÃO É DELIBERADAMENTE MÍNIMA. Retira-se um BOM UTF-8 no INÍCIO e mais nada. Não se
// aceita UTF-16, não se adivinha codificação, não se apara espaço: qualquer uma dessas seria
// tolerância a sério, e tolerância a sério num ficheiro que carrega uma âncora de confiança é
// como se aceitam coisas que não deviam entrar. O que se corrige é um artefacto conhecido de uma
// ferramenta conhecida, e o resto continua a falhar como falhava.
//
// O selador corrigido já escreve sem BOM. Isto é a segunda rede, para os ficheiros que já foram
// escritos e para quem edite um à mão num editor que reponha o BOM.
// ---------------------------------------------------------------------------------------------

// bomUTF8 é a marca de ordem de bytes, na forma em que aparece num ficheiro UTF-8.
var bomUTF8 = []byte{0xEF, 0xBB, 0xBF}

// semBOM devolve `raw` sem um BOM UTF-8 inicial. Sem BOM, devolve `raw` intacto.
func semBOM(raw []byte) []byte {
	return bytes.TrimPrefix(raw, bomUTF8)
}
