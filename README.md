# websocket-skeleton

##### Binance and Bybit WS clients as references

Binance client sends to Redis OrderBook L1 data
Bybit client sends raw data

```
docker exec -it websocket-skeleton-redis-1 redis-cli SUBSCRIBE binance_orderbook
docker exec -it websocket-skeleton-redis-1 redis-cli PSUBSCRIBE exchange_raw:bybit
```
