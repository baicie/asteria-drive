# P2 设计门禁：直传上传

- 分支：`mvp/p2-upload`
- 状态：Accepted
- 日期：2026-08-02

## 目标

控制面协调 Multipart；文件字节由客户端直传对象存储。

## 设计产物

- ADR-0004 整文件不可变 Blob
- ADR-0005 StorageProvider / SeaweedFS
- ADR-0006 上传状态机
- ADR-0008 创建幂等与维护任务分阶段（SHOULD）

## 实现范围

上传会话、分片签名、完成事务、取消/过期、S3 adapter 与 memory storage fake。

## 出口

fake 垂直闭环；完成幂等；handler 不转发文件体；故障可对账。
