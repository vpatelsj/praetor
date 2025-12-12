
# Scenario 1 - Basic Rollout

```docker compose up --build```

```praectl get devices```

```praectl rollout create switch firmware-update --version 0.1 --command 'echo update firmware v1'```

```praectl rollout update switch firmware-update --version 0.1 --command 'echo update firmware v2'```




```praectl rollout create bmc firmware-update --version 0.1 --command 'echo update firmware v1'```



Get stuck in Running because it didnt match the selector
```praectl rollout create switch goes-nowhere --version 0.1 --selector rack=rack1 --command 'echo "update switch1"'```

BAD update
```praectl rollout update switch firmware-update --version 0.1 --selector role=switch --command 'sh -c "exit 1"'```   


## Scenario 2 - Rollout with selectors and failures

Install firmware v1 on rack1 switches only
```praectl rollout create switch rack1-firmware-update --version 0.1 --command 'echo update firmware v1 on rack1 switches' --selector rack=rack1```


Install bad firmware on rack2 switches
```praectl rollout create switch rack2-firmware-update --version 0.1 --selector role=switch --command 'sh -c "exit 1"' --selector rack=rack2```
Install good firmware on rack2 switches
```praectl rollout update switch rack2-firmware-update --version 0.1 --command 'echo update firmware v1 on rack2 switches' --selector rack=rack2```

Install firmware v3 on all switches
```praectl rollout create switch firmware-update --version 0.1 --command 'echo update firmware v3'```

## Scenario 3 - new device joining after sometime

create 11th switch

```
printf 'services:\n  switch11:\n    build: ./agent\n    command: ["praetor-agent-switch", "--device-id=switch11", "--manager-address=http://manager:8080", "--label=rack=rack1", "--label=site=dc1"]\n    depends_on: [manager]\n' \
| docker compose -f docker-compose.scenario1.yml -f - up -d switch11
```

```praectl describe device switch switch11```

```praectl rollout create switch switch11-bootstrap --version 0.1 --selector deviceId=switch11 --command 'echo update firmware v1 on rack1 switches'```

```praectl rollout create switch switch11-bootstrap --version 0.1 --selector deviceId=switch11 --command 'echo update firmware v1 on rack1 switches'```

```praectl rollout update switch firmware-update --version 0.1 --command 'echo update firmware v3'```