import copy
import json

default_config = """{
    "listeners": [
        {"iface": "127.0.0.1", "port": 8080}
    ]
}"""


class ProxyConfig:
    
    def __init__(self):
        self._listeners = [('127.0.0.1', '8080')]
        
    def load(self, fname):
        try:
            with open(fname, 'r') as f:
                config_info = json.loads(f.read())
        except IOError:
            config_info = json.loads(default_config)
            with open(fname, 'w') as f:
                f.write(default_config)
            
        # Listeners
        if 'listeners' in config_info:
            self._listeners = []
            for info in config_info['listeners']:
                if 'port' in info:
                    port = info['port']
                else:
                    port = 8080

                if 'interface' in info:
                    iface = info['interface']
                elif 'iface' in info:
                    iface = info['iface']
                else:
                    iface = '127.0.0.1'

                self._listeners.append((iface, port))
                
    @property
    def listeners(self):
        return copy.deepcopy(self._listeners)
    
    @listeners.setter
    def listeners(self, val):
        self._listeners = val
