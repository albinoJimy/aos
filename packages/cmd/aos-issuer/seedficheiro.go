package main

import (
	"bytes"
	"errors"
	"strings"
)

// ---------------------------------------------------------------------------------------------
// AS SEEDS DOS OPERADORES NASCEM EM WINDOWS — E O WINDOWS ACRESCENTA COISAS.
//
// COMO APARECEU. O Jimy tentou gerar a chave do selador com `openssl rand -hex 32 > ficheiro` e o
// `openssl` não estava no PATH do PowerShell. Ao preparar a alternativa reparei num defeito que
// ele ainda não tinha encontrado: o redireccionamento `>` do PowerShell escreve BOM, e o
// `strings.TrimSpace` do Go NÃO remove um BOM — U+FEFF deixou de contar como espaço. A chave
// nasceria ilegível com «nao e hex», e ninguém liga essa mensagem a codificação.
//
// PORQUE ISTO É UM HELPER E NÃO UMA CORRECÇÃO NUM SÍTIO. Havia TRÊS carregadores de ficheiro com
// o mesmo padrão: [loadApproverKey] (que serve approve-sign, ratify-sign, autonomy-sign e
// worm-seal), [loadOrCreateKey] (a `issuer.key` — a chave que cunha TODAS as NHIs) e o do nó, em
// `cli.go`. Endurecer só o primeiro deixava de fora a mais importante das três, e seria o erro
// «unidade certa, cablagem errada» outra vez.
//
// O QUE SE LIMPA E O QUE NÃO SE LIMPA. Retira-se um BOM UTF-8 e espaço à volta. NÃO se aceita
// UTF-16: seria tolerância a sério num ficheiro que é uma chave privada. O que se faz ao UTF-16 é
// DIAGNOSTICÁ-LO — porque a diferença entre «o teu ficheiro não é hex» e «o teu ficheiro está em
// UTF-16» é a diferença entre uma hora perdida e trinta segundos.
// ---------------------------------------------------------------------------------------------

// ErrSeedUTF16 — o ficheiro da seed está em UTF-16. Não se converte: diz-se.
var ErrSeedUTF16 = errors.New("aos-issuer: o ficheiro da seed esta em UTF-16, nao em texto simples — o `>` e o `Out-File` do PowerShell fazem isto. Reescreva-o com [IO.File]::WriteAllText(caminho, hex), que grava UTF-8 sem BOM")

var (
	bomUTF8    = []byte{0xEF, 0xBB, 0xBF}
	bomUTF16LE = []byte{0xFF, 0xFE}
	bomUTF16BE = []byte{0xFE, 0xFF}
)

// limparSeedHex devolve o texto hex de um ficheiro de seed, ou explica porque não consegue.
func limparSeedHex(raw []byte) (string, error) {
	// UTF-16 COM BOM. Um ficheiro hex legítimo começa sempre por [0-9a-f]; 0xFF/0xFE nunca.
	if bytes.HasPrefix(raw, bomUTF16LE) || bytes.HasPrefix(raw, bomUTF16BE) {
		return "", ErrSeedUTF16
	}
	limpo := bytes.TrimPrefix(raw, bomUTF8)
	// UTF-16 SEM BOM. Não há prefixo que o denuncie, mas há os NUL intercalados — e um ficheiro
	// de texto hex não tem NUL nenhum. Sem este ramo, o caso mais comum do `Out-File` cairia no
	// «nao e hex» genérico, que é exactamente o diagnóstico que este helper existe para evitar.
	if bytes.IndexByte(limpo, 0x00) >= 0 {
		return "", ErrSeedUTF16
	}
	return strings.TrimSpace(string(limpo)), nil
}
