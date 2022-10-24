/*
Package forkchoice implements the service to support fork choice for the Ethereum beacon chain. This contains the
necessary components to track latest validators votes, and balances. Then a store object to be used
to calculate head. High level fork choice summary:
https://notes.ethereum.org/@vbuterin/rkhCgQteN?type=view#LMD-GHOST-fork-choice-rule
forkchoice包实现了service用于支持Ethereum beacon chain的fork choice，它包含了必要的组件用于追踪最新的validators votes
以及balance，之后一个store object用于计算head
*/
package forkchoice
