// Package runtime contém o contrato canónico do Agent Runtime partilhado com os
// componentes do data-plane (cache de prompt, gateway, memória).
//
// Vive no substrato para que platform/* possa materializar prompts e adaptar
// modelos sem importar a implementação completa do kernel/agent-runtime.
package runtime
