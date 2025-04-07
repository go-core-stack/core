#!/bin/bash

sudo docker stop mongo-express > /dev/null 2>&1
sudo docker rm mongo-express > /dev/null 2>&1

sudo docker stop mongodb > /dev/null 2>&1
sudo docker rm mongodb > /dev/null 2>&1
echo "cleanup done"