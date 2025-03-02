# 订阅合并转换检测工具

对比原项目是修复了一些逻辑、简化了一些东西、增加了一些功能

## 预览

![preview](./doc/images/preview.png)
![result](./doc/images/results.jpg)

## 功能

- 检测节点可用性,去除不可用节点
- ~~检测平台解锁情况~~ 暂时注释了，因为我觉得没啥用
  - ~~openai~~
  - ~~youtube~~
  - ~~netflix~~
  - ~~disney~~
- 合并多个订阅
- 将订阅转换为clash/mihomo/base64格式
- 节点去重
- 节点重命名
- 节点测速（单线程）
- 根据解锁情况分类保存
- 支持外部拉取结果
- 支持获取订阅及保存到gist使用socks及http代理

## 特点

- 支持多平台
- 支持多线程
- 资源占用低

## TODO

- [X] 适配多种订阅格式
- [ ] 支持更多的保存方式
  - [X] 本地
  - [X] cloudflare r2
  - [X] gist
  - [X] webdav
  - [X] http server
  - [ ] 其他
- [ ] 已知从clash格式转base64时vmess节点会丢失。因为太麻烦了，我不想处理了。

## 使用方法

> ~~如果拉取订阅速度慢，可使用通用的 `HTTP_PROXY` `HTTPS_PROXY` 环境变量加快速度；此变量不会影响节点测试速度~~
>
> 代理订阅地址和保存到gist的请求,支持 http/socks5 , 启用代理 `proxy-type` 和 `proxy-url` 必须填写。
>
> ```yaml
> # 不使用代理请留空，使用则填写代理协议，如socks
> proxy-type: ""
> # 代理地址，填写规则如下
> # 无需认证代理：http://host:port 或 socks5://host:port
> # 需要认证代理：http://username:password@host:prot 或 socks5://username:password@host:prot 
> proxy-url: ""
> ```

### docker运行

```bash
docker run -d --name subs-check -p 8199:8199 -v ./config:/app/config  -v ./output:/app/output --restart always ghcr.io/beck-8/subs-check:latest
```

### docker-compose

```yaml
version: "3"
services:
  mihomo-check:
    image: ghcr.io/beck-8/subs-check:latest
    container_name: subs-check
    volumes:
      - ./config:/app/config
      - ./output:/app/output
    ports:
      - "8199:8199"
    environment:
      - TZ=Asia/Shanghai
    restart: always
    tty: true
    network_mode: bridge
```

### 源码直接运行

```bash
go run main.go -f /path/to/config.yaml
```

### 二进制文件运行

直接运行即可,会在当前目录生成配置文件

## 保存方法配置

- 本地保存: 将结果保存到本地,默认保存到可执行文件目录下的 output 文件夹 ，在配置文件配置 `local-output-path`可设置存储路径，配置规则如下：
  ```plaintext
  windows 示例: "C:\\Users\\name\\subs-check\\temp"
  linux 示例: "/root/subs-check/temp"
  ```
- r2: 将结果保存到 cloudflare r2 存储桶 [配置方法](./doc/r2.md)
- gist: 将结果保存到 github gist [配置方法](./doc/gist.md)
- webdav: 将结果保存到 webdav 服务器 [配置方法](./doc/webdav.md)

## 对外提供服务配置

在配置文件中设置 `http-server` 为 `true` 可开启http服务，默认为开启，如需关闭改为 `false` 即可；`listen-port` 可设置开放的端口，直接填入端口号即可，留空为 `8199 `。使用方式如下：

- `http://127.0.0.1:8199/sub/all.yaml` 返回yaml格式节点。
- `http://127.0.0.1:8199/sub/all.txt` 返回base64格式节点。

可以直接将base64格式订阅放到V2rayN中
![subset](./doc/images/subset.jpeg)
![nodeinfo](./doc/images/nodeinfo.jpeg)

## 订阅使用方法

推荐直接裸核运行 tun 模式

原作者写的Windows下的裸核运行应用 [minihomo](https://github.com/bestruirui/minihomo)

- 下载[base.yaml](./doc/base.yaml)
- 将文件中对应的链接改为自己的即可

例如:

```yaml
proxy-providers:
  ProviderALL:
    url: https:// #将此处替换为自己的链接
    type: http
    interval: 600
    proxy: DIRECT
    health-check:
      enable: true
      url: http://www.google.com/generate_204
      interval: 60
    path: ./proxy_provider/ALL.yaml
```
